// Package aigateway provides a high-performance, zero-dependency AI gateway
// for routing requests to large language model (LLM) providers.
//
// The Gateway type is the main entry point: create one with New, register
// providers with RegisterProvider, load plugins from config with LoadPlugins,
// and route requests with Route or RouteStream.
//
// Plugins and routing strategies (single, fallback, load-balance, conditional)
// are configured via [Config] which can be loaded from a YAML or JSON file
// using [LoadConfig].
package aigateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/circuitbreaker"
	"github.com/ferro-labs/ai-gateway/internal/logging"
	"github.com/ferro-labs/ai-gateway/internal/metrics"
	"github.com/ferro-labs/ai-gateway/internal/strategies"
	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
)

// EventHookFunc is called asynchronously after a gateway event (request
// completed or failed). It replaces the old EventPublisher interface with a
// simpler function-based hook pattern.
type EventHookFunc func(ctx context.Context, subject string, data map[string]interface{})

// Gateway is the main entry point for routing LLM requests.
type Gateway struct {
	mu               sync.RWMutex
	config           Config
	catalog          models.Catalog
	providers        map[string]providers.Provider
	strategy         strategies.Strategy
	plugins          *plugin.Manager
	hooks            []EventHookFunc
	circuitBreakers  map[string]*circuitbreaker.CircuitBreaker
	discoveredModels map[string][]providers.ModelInfo
}

// New creates a new Gateway instance with the given configuration.
func New(cfg Config) (*Gateway, error) {
	catalog, err := models.Load()
	if err != nil {
		// Non-fatal: operate without model metadata (no enrichment / cost reporting).
		catalog = models.Catalog{}
	}
	return &Gateway{
		config:           cfg,
		catalog:          catalog,
		providers:        make(map[string]providers.Provider),
		plugins:          plugin.NewManager(),
		circuitBreakers:  make(map[string]*circuitbreaker.CircuitBreaker),
		discoveredModels: make(map[string][]providers.ModelInfo),
	}, nil
}

// Catalog returns a shallow copy of the loaded model catalog.
// A copy is returned so callers cannot mutate the gateway's internal catalog.
func (g *Gateway) Catalog() models.Catalog {
	g.mu.RLock()
	defer g.mu.RUnlock()
	cp := make(models.Catalog, len(g.catalog))
	maps.Copy(cp, g.catalog)
	return cp
}

// Event subject constants used when invoking gateway hooks.
const (
	SubjectRequestCompleted = "gateway.request.completed"
	SubjectRequestFailed    = "gateway.request.failed"
)

// RegisterProvider registers a provider with the gateway.
func (g *Gateway) RegisterProvider(p providers.Provider) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.providers[p.Name()] = p
	g.strategy = nil // force strategy rebuild
}

// RegisterPlugin registers a plugin at the given lifecycle stage.
func (g *Gateway) RegisterPlugin(stage plugin.Stage, p plugin.Plugin) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.plugins.Register(stage, p)
}

// AddHook registers an EventHookFunc that is called asynchronously on each
// completed or failed request. Multiple hooks may be registered; all are
// invoked for every event.
func (g *Gateway) AddHook(fn EventHookFunc) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.hooks = append(g.hooks, fn)
}

// Route routes a request to the appropriate provider based on the configuration.
func (g *Gateway) Route(ctx context.Context, req providers.Request) (*providers.Response, error) {
	start := time.Now()
	log := logging.FromContext(ctx)

	// Resolve model alias before routing.
	req = g.resolveAlias(req)

	s, err := g.getStrategy()
	if err != nil {
		return nil, err
	}

	// Run before-request plugins (guardrails, transforms, rate-limit).
	pctx := plugin.NewContext(&req)
	if g.plugins.HasPlugins() {
		if err := g.plugins.RunBefore(ctx, pctx); err != nil {
			metrics.RequestsTotal.WithLabelValues("", req.Model, "rejected").Inc()
			return nil, err
		}
	}

	// Execute the strategy (provider selection + actual call).
	resp, err := s.Execute(ctx, req)
	latency := time.Since(start)

	if err != nil {
		pctx.Error = err
		g.plugins.RunOnError(ctx, pctx)

		provider := ""
		errType := "provider_error"
		if errors.Is(err, circuitbreaker.ErrCircuitOpen) {
			errType = "circuit_open"
		}
		metrics.RequestsTotal.WithLabelValues(provider, req.Model, "error").Inc()
		metrics.ProviderErrors.WithLabelValues(provider, errType).Inc()

		log.Error("request failed",
			"model", req.Model,
			"latency_ms", latency.Milliseconds(),
			"error", err.Error(),
		)

		g.publishEvent(ctx, SubjectRequestFailed, map[string]interface{}{
			"trace_id":   logging.TraceIDFromContext(ctx),
			"model":      req.Model,
			"error":      err.Error(),
			"status":     500,
			"latency_ms": latency.Milliseconds(),
			"timestamp":  time.Now(),
		})
		return nil, err
	}

	// Ensure OpenAI-compatible envelope fields are always set.
	if resp.Object == "" {
		resp.Object = "chat.completion"
	}
	if resp.Created == 0 {
		resp.Created = time.Now().Unix()
	}

	// Run after-request plugins (logging, caching).
	if g.plugins.HasPlugins() {
		pctx.Response = resp
		_ = g.plugins.RunAfter(ctx, pctx)
	}

	// Emit Prometheus metrics.
	metrics.RequestDuration.WithLabelValues(resp.Provider, resp.Model).Observe(latency.Seconds())
	metrics.RequestsTotal.WithLabelValues(resp.Provider, resp.Model, "success").Inc()
	metrics.TokensInput.WithLabelValues(resp.Provider, resp.Model).Add(float64(resp.Usage.PromptTokens))
	metrics.TokensOutput.WithLabelValues(resp.Provider, resp.Model).Add(float64(resp.Usage.CompletionTokens))

	// Emit cost metrics using the model catalog.
	g.mu.RLock()
	catalog := g.catalog
	g.mu.RUnlock()
	cost := models.Calculate(catalog, resp.Provider+"/"+resp.Model, models.Usage{
		PromptTokens:     resp.Usage.PromptTokens,
		CompletionTokens: resp.Usage.CompletionTokens,
		ReasoningTokens:  resp.Usage.ReasoningTokens,
		CacheReadTokens:  resp.Usage.CacheReadTokens,
		CacheWriteTokens: resp.Usage.CacheWriteTokens,
	})
	if cost.TotalUSD > 0 {
		metrics.RequestCostUSD.WithLabelValues(resp.Provider, resp.Model).Add(cost.TotalUSD)
	}

	log.Info("request completed",
		"model", resp.Model,
		"provider", resp.Provider,
		"latency_ms", latency.Milliseconds(),
		"tokens_in", resp.Usage.PromptTokens,
		"tokens_out", resp.Usage.CompletionTokens,
		"cost_usd", cost.TotalUSD,
	)

	g.publishEvent(ctx, SubjectRequestCompleted, map[string]interface{}{
		"trace_id":             resp.ID,
		"provider":             resp.Provider,
		"model":                resp.Model,
		"status":               200,
		"latency_ms":           latency.Milliseconds(),
		"tokens_in":            resp.Usage.PromptTokens,
		"tokens_out":           resp.Usage.CompletionTokens,
		"cost_usd":             cost.TotalUSD,
		"cost_input_usd":       cost.InputUSD,
		"cost_output_usd":      cost.OutputUSD,
		"cost_cache_read_usd":  cost.CacheReadUSD,
		"cost_cache_write_usd": cost.CacheWriteUSD,
		"cost_reasoning_usd":   cost.ReasoningUSD,
		"cost_image_usd":       cost.ImageUSD,
		"cost_audio_usd":       cost.AudioUSD,
		"cost_embedding_usd":   cost.EmbeddingUSD,
		"cost_model_found":     cost.ModelFound,
		"timestamp":            time.Now(),
	})

	return resp, nil
}

// publishEvent calls all registered hooks asynchronously.
func (g *Gateway) publishEvent(ctx context.Context, subject string, data map[string]interface{}) {
	g.mu.RLock()
	hooks := make([]EventHookFunc, len(g.hooks))
	copy(hooks, g.hooks)
	g.mu.RUnlock()

	for _, h := range hooks {
		fn := h
		go fn(ctx, subject, data)
	}
}

// ReloadConfig validates and applies a new configuration, forcing strategy rebuild on next request.
func (g *Gateway) ReloadConfig(cfg Config) error {
	if err := ValidateConfig(cfg); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.config = cfg
	g.strategy = nil // force rebuild on next request
	g.circuitBreakers = make(map[string]*circuitbreaker.CircuitBreaker)
	return nil
}

// GetConfig returns a copy of the current configuration.
func (g *Gateway) GetConfig() Config {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.config
}

// getStrategy lazily builds the strategy from config and registered providers.
// Circuit breakers are built once and applied in the provider lookup closure.
func (g *Gateway) getStrategy() (strategies.Strategy, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.strategy != nil {
		return g.strategy, nil
	}

	// Build circuit breakers for targets that have them configured.
	for _, t := range g.config.Targets {
		if t.CircuitBreaker == nil {
			continue
		}
		if _, exists := g.circuitBreakers[t.VirtualKey]; exists {
			continue
		}
		timeout, _ := time.ParseDuration(t.CircuitBreaker.Timeout)
		cb := circuitbreaker.New(t.CircuitBreaker.FailureThreshold, t.CircuitBreaker.SuccessThreshold, timeout)
		g.circuitBreakers[t.VirtualKey] = cb
	}

	// Provider lookup with transparent circuit-breaker wrapping.
	lookup := func(name string) (providers.Provider, bool) {
		p, ok := g.providers[name]
		if !ok {
			return nil, false
		}
		if cb, hasCB := g.circuitBreakers[name]; hasCB {
			return &cbProvider{Provider: p, cb: cb, name: name}, true
		}
		return p, ok
	}

	targets := make([]strategies.Target, len(g.config.Targets))
	for i, t := range g.config.Targets {
		targets[i] = strategies.Target{
			VirtualKey: t.VirtualKey,
			Weight:     t.Weight,
		}
	}

	var s strategies.Strategy
	switch g.config.Strategy.Mode {
	case ModeSingle, "":
		if len(targets) == 0 {
			return nil, fmt.Errorf("no targets configured for single strategy")
		}
		s = strategies.NewSingle(targets[0], lookup)
	case ModeFallback:
		fb := strategies.NewFallback(targets, lookup)
		if len(g.config.Targets) > 0 && g.config.Targets[0].Retry != nil {
			fb.WithMaxRetries(g.config.Targets[0].Retry.Attempts)
		}
		s = fb
	case ModeLoadBalance:
		s = strategies.NewLoadBalance(targets, lookup)
	case ModeConditional:
		if len(g.config.Strategy.Conditions) == 0 {
			return nil, fmt.Errorf("no conditions configured for conditional strategy")
		}
		if len(targets) == 0 {
			return nil, fmt.Errorf("no targets configured for conditional strategy")
		}
		var rules []strategies.ConditionRule
		for _, cond := range g.config.Strategy.Conditions {
			rules = append(rules, strategies.ConditionRule{
				Key:    cond.Key,
				Value:  cond.Value,
				Target: strategies.Target{VirtualKey: cond.TargetKey},
			})
		}
		s = strategies.NewConditional(rules, targets[0], lookup)
	default:
		return nil, fmt.Errorf("unknown strategy mode: %s", g.config.Strategy.Mode)
	}

	g.strategy = s
	return s, nil
}

// cbProvider wraps a Provider with a circuit breaker.
type cbProvider struct {
	providers.Provider
	cb   *circuitbreaker.CircuitBreaker
	name string
}

func (p *cbProvider) Complete(ctx context.Context, req providers.Request) (*providers.Response, error) {
	if !p.cb.Allow() {
		metrics.CircuitBreakerState.WithLabelValues(p.name).Set(1) // open
		return nil, circuitbreaker.ErrCircuitOpen
	}
	resp, err := p.Provider.Complete(ctx, req)
	if err != nil {
		p.cb.RecordFailure()
		metrics.CircuitBreakerState.WithLabelValues(p.name).Set(float64(p.cb.State()))
		return nil, err
	}
	p.cb.RecordSuccess()
	metrics.CircuitBreakerState.WithLabelValues(p.name).Set(0) // closed
	return resp, nil
}

func (p *cbProvider) CompleteStream(ctx context.Context, req providers.Request) (<-chan providers.StreamChunk, error) {
	if !p.cb.Allow() {
		metrics.CircuitBreakerState.WithLabelValues(p.name).Set(1) // open
		return nil, circuitbreaker.ErrCircuitOpen
	}
	sp, ok := p.Provider.(providers.StreamProvider)
	if !ok {
		return nil, fmt.Errorf("provider %s does not support streaming", p.name)
	}
	ch, err := sp.CompleteStream(ctx, req)
	if err != nil {
		p.cb.RecordFailure()
		metrics.CircuitBreakerState.WithLabelValues(p.name).Set(float64(p.cb.State()))
		return nil, err
	}
	p.cb.RecordSuccess()
	metrics.CircuitBreakerState.WithLabelValues(p.name).Set(0)
	return ch, nil
}

// LoadPlugins initializes and registers plugins from the gateway configuration.
func (g *Gateway) LoadPlugins() error {
	for _, pc := range g.config.Plugins {
		if !pc.Enabled {
			continue
		}
		factory, ok := plugin.GetFactory(pc.Name)
		if !ok {
			return fmt.Errorf("unknown plugin: %s", pc.Name)
		}
		p := factory()
		if err := p.Init(pc.Config); err != nil {
			return fmt.Errorf("plugin %s init failed: %w", pc.Name, err)
		}
		stage := plugin.Stage(pc.Stage)
		if err := g.RegisterPlugin(stage, p); err != nil {
			return fmt.Errorf("plugin %s register failed: %w", pc.Name, err)
		}
	}
	return nil
}

// RouteStream runs before-request plugins then returns a streaming response channel.
// Provider resolution follows the configured strategy mode, then falls back to any
// registered provider that supports the requested model and streaming.
func (g *Gateway) RouteStream(ctx context.Context, req providers.Request) (<-chan providers.StreamChunk, error) {
	log := logging.FromContext(ctx)

	// Resolve model alias before routing.
	req = g.resolveAlias(req)

	// Run before-request plugins (word-filter, max-token, rate-limit, etc.).
	pctx := plugin.NewContext(&req)
	if g.plugins.HasPlugins() {
		if err := g.plugins.RunBefore(ctx, pctx); err != nil {
			metrics.RequestsTotal.WithLabelValues("", req.Model, "rejected").Inc()
			return nil, err
		}
		if pctx.Reject {
			metrics.RequestsTotal.WithLabelValues("", req.Model, "rejected").Inc()
			return nil, fmt.Errorf("request rejected by plugin: %s", pctx.Reason)
		}
	}
	// Propagate any modifications made by plugins (e.g., capped max_tokens).
	req = *pctx.Request

	// Resolve provider according to strategy mode.
	g.mu.RLock()
	orderedKeys := g.streamingTargetOrderLocked(req)
	var sp providers.StreamProvider
	for _, key := range orderedKeys {
		p, ok := g.providers[key]
		if !ok || !p.SupportsModel(req.Model) {
			continue
		}
		// Apply circuit breaker if configured.
		candidate := p
		if cb, hasCB := g.circuitBreakers[key]; hasCB {
			candidate = &cbProvider{Provider: p, cb: cb, name: key}
		}
		if casted, ok := candidate.(providers.StreamProvider); ok {
			sp = casted
			break
		}
	}
	// Fallback: any registered provider that supports this model and streaming.
	if sp == nil {
		for key, p := range g.providers {
			if !p.SupportsModel(req.Model) {
				continue
			}
			candidate := p
			if cb, hasCB := g.circuitBreakers[key]; hasCB {
				candidate = &cbProvider{Provider: p, cb: cb, name: key}
			}
			if casted, ok := candidate.(providers.StreamProvider); ok {
				sp = casted
				break
			}
		}
	}
	g.mu.RUnlock()

	if sp == nil {
		return nil, fmt.Errorf("no streaming-capable provider found for model: %s", req.Model)
	}

	log.Info("stream request started", "model", req.Model)
	return sp.CompleteStream(ctx, req)
}

func (g *Gateway) streamingTargetOrderLocked(req providers.Request) []string {
	targets := g.config.Targets
	if len(targets) == 0 {
		return nil
	}

	switch g.config.Strategy.Mode {
	case ModeSingle, "":
		return []string{targets[0].VirtualKey}
	case ModeFallback:
		keys := make([]string, 0, len(targets))
		for _, t := range targets {
			keys = append(keys, t.VirtualKey)
		}
		return keys
	case ModeConditional:
		keys := make([]string, 0, len(targets))
		for _, cond := range g.config.Strategy.Conditions {
			if conditionMatches(cond, req.Model) {
				keys = appendUniqueKey(keys, cond.TargetKey)
				break
			}
		}
		for _, t := range targets {
			keys = appendUniqueKey(keys, t.VirtualKey)
		}
		return keys
	case ModeLoadBalance:
		startIdx := weightedStartIndex(targets)
		keys := make([]string, 0, len(targets))
		for i := 0; i < len(targets); i++ {
			keys = append(keys, targets[(startIdx+i)%len(targets)].VirtualKey)
		}
		return keys
	default:
		keys := make([]string, 0, len(targets))
		for _, t := range targets {
			keys = append(keys, t.VirtualKey)
		}
		return keys
	}
}

func conditionMatches(cond Condition, model string) bool {
	switch cond.Key {
	case "model":
		return model == cond.Value
	case "model_prefix":
		return strings.HasPrefix(model, cond.Value)
	default:
		return false
	}
}

func appendUniqueKey(keys []string, key string) []string {
	if key == "" {
		return keys
	}
	for _, existing := range keys {
		if existing == key {
			return keys
		}
	}
	return append(keys, key)
}

func weightedStartIndex(targets []Target) int {
	if len(targets) == 0 {
		return 0
	}

	totalWeight := 0.0
	for _, t := range targets {
		w := t.Weight
		if w <= 0 {
			w = 1
		}
		totalWeight += w
	}
	if totalWeight <= 0 {
		return 0
	}

	r := rand.Float64() * totalWeight //nolint:gosec
	cumulative := 0.0
	for i, t := range targets {
		w := t.Weight
		if w <= 0 {
			w = 1
		}
		cumulative += w
		if r < cumulative {
			return i
		}
	}

	return len(targets) - 1
}

// ── Registry-consolidation helpers ──────────────────────────────────────────
// These methods make *Gateway satisfy providers.ProviderSource so that HTTP
// handlers that previously held a *providers.Registry can accept the gateway
// directly instead.

// AllModels returns ModelInfo from all registered providers.
// If auto-discovery has run for a provider, discovered models take precedence
// over the provider's static model list.
func (g *Gateway) AllModels() []providers.ModelInfo {
	g.mu.RLock()
	defer g.mu.RUnlock()
	var models []providers.ModelInfo
	for name, p := range g.providers {
		if discovered, ok := g.discoveredModels[name]; ok && len(discovered) > 0 {
			models = append(models, discovered...)
		} else {
			models = append(models, p.Models()...)
		}
	}
	return models
}

// GetProvider returns a registered provider by name.
func (g *Gateway) GetProvider(name string) (providers.Provider, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	p, ok := g.providers[name]
	return p, ok
}

// Get satisfies providers.ProviderSource (alias for GetProvider).
func (g *Gateway) Get(name string) (providers.Provider, bool) {
	return g.GetProvider(name)
}

// ListProviders returns the names of all registered providers.
func (g *Gateway) ListProviders() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	names := make([]string, 0, len(g.providers))
	for name := range g.providers {
		names = append(names, name)
	}
	return names
}

// List satisfies providers.ProviderSource (alias for ListProviders).
func (g *Gateway) List() []string {
	return g.ListProviders()
}

// FindByModel returns the first registered provider that supports the given model.
func (g *Gateway) FindByModel(model string) (providers.Provider, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, p := range g.providers {
		if p.SupportsModel(model) {
			return p, true
		}
	}
	return nil, false
}

// Close cleans up resources.
func (g *Gateway) Close() error {
	return nil
}

// ── Alias resolution ─────────────────────────────────────────────────────────

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

// ── Multi-modal endpoints ────────────────────────────────────────────────────

// Embed routes an embedding request to the first registered EmbeddingProvider
// that supports the requested model.
func (g *Gateway) Embed(ctx context.Context, req providers.EmbeddingRequest) (*providers.EmbeddingResponse, error) {
	log := logging.FromContext(ctx)

	// Resolve model alias so embedding endpoints honour the same aliases as chat.
	req.Model = g.resolveModelAlias(req.Model)

	g.mu.RLock()
	var ep providers.EmbeddingProvider
	for _, p := range g.providers {
		if ep2, ok := p.(providers.EmbeddingProvider); ok && p.SupportsModel(req.Model) {
			ep = ep2
			break
		}
	}
	g.mu.RUnlock()

	if ep == nil {
		return nil, fmt.Errorf("no embedding provider found for model: %s", req.Model)
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
	var ip providers.ImageProvider
	for _, p := range g.providers {
		if ip2, ok := p.(providers.ImageProvider); ok && p.SupportsModel(req.Model) {
			ip = ip2
			break
		}
	}
	g.mu.RUnlock()

	if ip == nil {
		return nil, fmt.Errorf("no image generation provider found for model: %s", req.Model)
	}

	resp, err := ip.GenerateImage(ctx, req)
	if err != nil {
		log.Error("image generation request failed", "model", req.Model, "error", err.Error())
		return nil, err
	}

	log.Info("image generation request completed", "model", req.Model, "images", len(resp.Data))
	return resp, nil
}

// ── Auto-discovery ───────────────────────────────────────────────────────────

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
		g.mu.Unlock()
		log.Info("model discovery completed", "provider", name, "models", len(models))
	}
}
