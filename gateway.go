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
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/strategies"
	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
)

// EventPublisher is satisfied by any event bus implementation.
// Any type with a Publish method can be used for event publishing.
type EventPublisher interface {
	Publish(ctx context.Context, subject string, payload interface{}) error
}

// Gateway is the main entry point for routing LLM requests.
type Gateway struct {
	mu        sync.RWMutex
	config    Config
	providers map[string]providers.Provider
	strategy  strategies.Strategy
	plugins   *plugin.Manager
	events    EventPublisher
}

// New creates a new Gateway instance with the given configuration.
func New(cfg Config) (*Gateway, error) {
	return &Gateway{
		config:    cfg,
		providers: make(map[string]providers.Provider),
		plugins:   plugin.NewManager(),
	}, nil
}

// Event subject constants used when publishing gateway lifecycle events.
const (
	SubjectRequestCompleted = "gateway.request.completed"
	SubjectRequestFailed    = "gateway.request.failed"
)

// RegisterProvider registers a provider with the gateway.
func (g *Gateway) RegisterProvider(p providers.Provider) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.providers[p.Name()] = p
	g.strategy = nil
}

// RegisterPlugin registers a plugin at the given lifecycle stage.
func (g *Gateway) RegisterPlugin(stage plugin.Stage, p plugin.Plugin) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.plugins.Register(stage, p)
}

// SetEventsPublisher sets the event publisher for the gateway.
func (g *Gateway) SetEventsPublisher(p EventPublisher) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.events = p
}

// Route routes a request to the appropriate provider based on the configuration.
func (g *Gateway) Route(ctx context.Context, req providers.Request) (*providers.Response, error) {
	start := time.Now()
	s, err := g.getStrategy()
	if err != nil {
		return nil, err
	}

	// Run before-request plugins.
	pctx := plugin.NewContext(&req)
	if g.plugins.HasPlugins() {
		if err := g.plugins.RunBefore(ctx, pctx); err != nil {
			return nil, err
		}
	}

	// Execute the strategy.
	resp, err := s.Execute(ctx, req)
	latency := int(time.Since(start).Milliseconds())

	if err != nil {
		pctx.Error = err
		g.plugins.RunOnError(ctx, pctx)

		// Publish failure event if NATS is enabled
		g.publishFailureEvent(ctx, req, err, latency)

		return nil, err
	}

	// Ensure OpenAI-compatible envelope fields are always set.
	if resp.Object == "" {
		resp.Object = "chat.completion"
	}
	if resp.Created == 0 {
		resp.Created = time.Now().Unix()
	}

	// Run after-request plugins.
	if g.plugins.HasPlugins() {
		pctx.Response = resp
		_ = g.plugins.RunAfter(ctx, pctx)
	}

	// Publish completion event if NATS is enabled
	g.publishCompletionEvent(ctx, req, resp, latency)

	return resp, nil
}

func (g *Gateway) publishCompletionEvent(_ context.Context, _ providers.Request, resp *providers.Response, latencyMS int) {
	g.mu.RLock()
	pub := g.events
	g.mu.RUnlock()

	if pub == nil {
		return
	}

	payload := map[string]interface{}{
		"trace_id":   resp.ID,
		"provider":   resp.Provider,
		"model":      resp.Model,
		"status":     200,
		"latency_ms": latencyMS,
		"tokens_in":  resp.Usage.PromptTokens,
		"tokens_out": resp.Usage.CompletionTokens,
		"timestamp":  time.Now(),
	}

	go func() {
		pub.Publish(context.Background(), SubjectRequestCompleted, payload) //nolint:errcheck,gosec
	}()
}

func (g *Gateway) publishFailureEvent(_ context.Context, req providers.Request, err error, latencyMS int) {
	g.mu.RLock()
	pub := g.events
	g.mu.RUnlock()

	if pub == nil {
		return
	}

	payload := map[string]interface{}{
		"trace_id":   "",
		"provider":   "",
		"model":      req.Model,
		"error":      err.Error(),
		"status":     500,
		"latency_ms": latencyMS,
		"timestamp":  time.Now(),
	}

	go func() {
		pub.Publish(context.Background(), SubjectRequestFailed, payload) //nolint:errcheck,gosec
	}()
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
	return nil
}

// GetConfig returns a copy of the current configuration.
func (g *Gateway) GetConfig() Config {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.config
}

// getStrategy lazily builds the strategy from config and registered providers.
func (g *Gateway) getStrategy() (strategies.Strategy, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.strategy != nil {
		return g.strategy, nil
	}

	lookup := func(name string) (providers.Provider, bool) {
		p, ok := g.providers[name]
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
	// Run before-request plugins (word-filter, max-token, etc.).
	pctx := plugin.NewContext(&req)
	if g.plugins.HasPlugins() {
		if err := g.plugins.RunBefore(ctx, pctx); err != nil {
			return nil, err
		}
		if pctx.Reject {
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
		if p, ok := g.providers[key]; ok && p.SupportsModel(req.Model) {
			if casted, ok := p.(providers.StreamProvider); ok {
				sp = casted
				break
			}
		}
	}
	// Fallback: any registered provider that supports this model and streaming.
	if sp == nil {
		for _, p := range g.providers {
			if p.SupportsModel(req.Model) {
				if casted, ok := p.(providers.StreamProvider); ok {
					sp = casted
					break
				}
			}
		}
	}
	g.mu.RUnlock()

	if sp == nil {
		return nil, fmt.Errorf("no streaming-capable provider found for model: %s", req.Model)
	}
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

// Close cleans up resources.
func (g *Gateway) Close() error {
	return nil
}
