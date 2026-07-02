package aigateway

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/logging"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// Gateway model alias resolution, the multi-modal (embedding / image) routing
// endpoints, and background model auto-discovery.

// resolveModelAlias returns the alias target for model, or model unchanged.
func (g *Gateway) resolveModelAlias(model string) string {
	g.mu.RLock()
	target, ok := g.config.Aliases[model]
	g.mu.RUnlock()
	if ok {
		return target
	}
	return model
}

// resolveAlias replaces req.Model with its configured alias target (if any).
func (g *Gateway) resolveAlias(req providers.Request) providers.Request {
	req.Model = g.resolveModelAlias(req.Model)
	return req
}

// Embed routes an embedding request to the first registered EmbeddingProvider
// that supports the requested model.
func (g *Gateway) Embed(ctx context.Context, req providers.EmbeddingRequest) (*providers.EmbeddingResponse, error) {
	log := logging.FromContext(ctx)

	// Resolve model alias so embedding endpoints honour the same aliases as chat.
	req.Model = g.resolveModelAlias(req.Model)

	g.mu.RLock()
	ep, ok := g.findEmbeddingProviderByModelLocked(req.Model)
	g.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("%w: no embedding provider for %q", core.ErrNoCapableProvider, req.Model)
	}

	resp, err := ep.Embed(ctx, req)
	if err != nil {
		log.Error("embedding request failed", "model", req.Model, "error", err.Error())
		return nil, err
	}

	log.Info("embedding request completed", "model", resp.Model, "tokens", resp.Usage.TotalTokens)
	return resp, nil
}

// GenerateImage routes an image generation request to the first registered
// ImageProvider that supports the requested model.
func (g *Gateway) GenerateImage(ctx context.Context, req providers.ImageRequest) (*providers.ImageResponse, error) {
	log := logging.FromContext(ctx)

	// Resolve model alias so image endpoints honour the same aliases as chat.
	req.Model = g.resolveModelAlias(req.Model)

	g.mu.RLock()
	ip, ok := g.findImageProviderByModelLocked(req.Model)
	g.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("%w: no image generation provider for %q", core.ErrNoCapableProvider, req.Model)
	}

	resp, err := ip.GenerateImage(ctx, req)
	if err != nil {
		log.Error("image generation request failed", "model", req.Model, "error", err.Error())
		return nil, err
	}

	log.Info("image generation request completed", "model", req.Model, "images", len(resp.Data))
	return resp, nil
}

// StartDiscovery periodically refreshes model lists from providers that implement
// DiscoveryProvider. It runs in a background goroutine until ctx is cancelled.
// interval must be greater than zero; an error is returned otherwise.
func (g *Gateway) StartDiscovery(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		return fmt.Errorf("StartDiscovery: interval must be greater than zero, got %v", interval)
	}
	log := logging.FromContext(ctx)
	go func() {
		g.runDiscovery(ctx, log)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				g.runDiscovery(ctx, log)
			}
		}
	}()
	return nil
}

func (g *Gateway) runDiscovery(ctx context.Context, log *slog.Logger) {
	g.mu.RLock()
	providersCopy := make(map[string]providers.Provider, len(g.providers))
	for k, v := range g.providers {
		providersCopy[k] = v
	}
	g.mu.RUnlock()

	for name, p := range providersCopy {
		dp, ok := p.(providers.DiscoveryProvider)
		if !ok {
			continue
		}
		models, err := dp.DiscoverModels(ctx)
		if err != nil {
			log.Error("model discovery failed", "provider", name, "error", err.Error())
			continue
		}
		g.mu.Lock()
		g.discoveredModels[name] = models
		g.rebuildModelIndexesLocked()
		g.mu.Unlock()
		log.Info("model discovery completed", "provider", name, "models", len(models))
	}
}
