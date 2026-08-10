package aigateway

// Background model auto-discovery: periodic refresh of provider model lists.

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/ferro-labs/ai-gateway/internal/redact"
	"github.com/ferro-labs/ai-gateway/pkg/logger"
	"github.com/ferro-labs/ai-gateway/providers"
)

// StartDiscovery periodically refreshes model lists from providers that implement
// DiscoveryProvider. It runs in a background goroutine until ctx is cancelled.
// interval must be greater than zero; an error is returned otherwise.
func (g *Gateway) StartDiscovery(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		return fmt.Errorf("StartDiscovery: interval must be greater than zero, got %v", interval)
	}
	log := g.log.Ctx(ctx)
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

// discoveryConcurrency bounds how many provider /models endpoints are polled at
// once. Discovery is entirely network-bound, so the serial walk this replaces
// cost the sum of every provider's round trip — with ~19 discoverable providers
// that is minutes of a refresh spent waiting, and a slow or hanging upstream
// delayed every provider queued behind it. The cap keeps a refresh from opening
// one connection per registered provider at the same instant.
const discoveryConcurrency = 8

// discoveryResult carries one provider's outcome back to the single writer.
type discoveryResult struct {
	name   string
	models []providers.ModelInfo
}

func (g *Gateway) runDiscovery(ctx context.Context, log *logger.Logger) {
	g.mu.RLock()
	discoverable := make(map[string]providers.DiscoveryProvider, len(g.providers))
	for name, p := range g.providers {
		if dp, ok := providers.As[providers.DiscoveryProvider](p); ok {
			discoverable[name] = dp
		}
	}
	g.mu.RUnlock()
	if len(discoverable) == 0 {
		return
	}

	// Each provider is polled concurrently and writes only its own slot, so the
	// fan-out needs no lock of its own. errgroup is used for its concurrency
	// limit and context plumbing; a failed provider is logged and skipped rather
	// than returned, because one unreachable upstream must not abandon the
	// refresh for every other provider.
	results := make([]discoveryResult, 0, len(discoverable))
	var mu sync.Mutex
	group, ctx := errgroup.WithContext(ctx)
	group.SetLimit(discoveryConcurrency)
	for name, dp := range discoverable {
		group.Go(func() error {
			models, err := dp.DiscoverModels(ctx)
			if err != nil {
				log.Error("model discovery failed", "provider", name, "error", redact.ErrorMessage(err))
				return nil
			}
			mu.Lock()
			results = append(results, discoveryResult{name: name, models: models})
			mu.Unlock()
			log.Info("model discovery completed", "provider", name, "models", len(models))
			return nil
		})
	}
	_ = group.Wait() // every goroutine returns nil; failures are logged above

	if len(results) == 0 {
		return
	}

	// One write and one index rebuild for the whole round. Rebuilding per
	// provider re-derived the catalog cache for every other provider each time,
	// so a refresh across N providers paid that N times over for a result only
	// the last pass kept.
	g.mu.Lock()
	for _, r := range results {
		g.discoveredModels[r.name] = r.models
	}
	g.rebuildModelIndexesLocked()
	g.mu.Unlock()
}
