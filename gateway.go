// Package aigateway provides a high-performance, zero-dependency AI gateway
// for routing requests to large language model (LLM) providers.
//
// The Gateway type is the main entry point: create one with New, register
// providers with RegisterProvider, load plugins from config with LoadPlugins,
// and route requests with Route or RouteStream.
//
// Plugins and routing strategies (single, fallback, load-balance, conditional,
// content-based, ab-test) are configured via [Config] which can be loaded
// from a YAML or JSON file using [LoadConfig].
package aigateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math/rand"
	"regexp"
	"runtime"
	"runtime/trace"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/circuitbreaker"
	"github.com/ferro-labs/ai-gateway/internal/events"
	"github.com/ferro-labs/ai-gateway/internal/latency"
	"github.com/ferro-labs/ai-gateway/internal/logging"
	"github.com/ferro-labs/ai-gateway/internal/mcp"
	"github.com/ferro-labs/ai-gateway/internal/metrics"
	"github.com/ferro-labs/ai-gateway/internal/strategies"
	"github.com/ferro-labs/ai-gateway/internal/streamwrap"
	pubmcp "github.com/ferro-labs/ai-gateway/mcp"
	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"
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
	providerNames    []string
	strategy         strategies.Strategy
	plugins          *plugin.Manager
	closeOnce        sync.Once
	hooks            []EventHookFunc
	hookSnapshot     atomic.Value
	hookDispatchQ    chan hookDispatch
	circuitBreakers  map[string]*circuitbreaker.CircuitBreaker
	discoveredModels map[string][]providers.ModelInfo
	latencyTracker   *latency.Tracker
	modelIndex       modelLookupIndex

	// MCP fields — nil when no MCPServers are configured.
	mcpRegistry *mcp.Registry
	mcpExecutor *mcp.Executor
	mcpInitDone chan struct{} // closed when background MCP init goroutine completes
}

type modelLookupIndex struct {
	exactProviders       map[string][]string
	exactStreamProviders map[string][]string
	exactEmbedProviders  map[string][]string
	exactImageProviders  map[string][]string
}

type hookDispatch struct {
	ctx   context.Context
	event events.HookEvent
	hook  EventHookFunc
}

const hookDispatchQueueSize = 256

// New creates a new Gateway instance with the given configuration.
func New(cfg Config) (*Gateway, error) {
	catalog, err := models.Load()
	if err != nil {
		// Non-fatal: operate without model metadata (no enrichment / cost reporting).
		catalog = models.Catalog{}
	}

	gw := &Gateway{
		config:           cfg,
		catalog:          catalog,
		providers:        make(map[string]providers.Provider),
		plugins:          plugin.NewManager(),
		circuitBreakers:  make(map[string]*circuitbreaker.CircuitBreaker),
		discoveredModels: make(map[string][]providers.ModelInfo),
		latencyTracker:   latency.New(0), // default window size (100 samples)
		modelIndex: modelLookupIndex{
			exactProviders:       make(map[string][]string),
			exactStreamProviders: make(map[string][]string),
			exactEmbedProviders:  make(map[string][]string),
			exactImageProviders:  make(map[string][]string),
		},
		hookDispatchQ: make(chan hookDispatch, hookDispatchQueueSize),
	}
	gw.hookSnapshot.Store([]EventHookFunc{})
	gw.startHookWorkers()

	// Wire MCP if any servers are configured.
	if len(cfg.MCPServers) > 0 {
		reg := mcp.NewRegistry()
		for _, mcpCfg := range cfg.MCPServers {
			reg.RegisterConfig(mcpCfg)
		}

		// Use the minimum positive MaxCallDepth across all servers; 0 lets
		// NewExecutor apply the default of 5.
		maxDepth := 0
		for _, mcpCfg := range cfg.MCPServers {
			if mcpCfg.MaxCallDepth > 0 && (maxDepth == 0 || mcpCfg.MaxCallDepth < maxDepth) {
				maxDepth = mcpCfg.MaxCallDepth
			}
		}

		gw.mcpRegistry = reg
		gw.mcpExecutor = mcp.NewExecutor(reg, maxDepth, buildMCPAuditFn(cfg.MCPToolCallAuditFn))

		// Handshake and tool discovery run in the background; New() returns
		// immediately. mcpInitDone is closed once initialization completes so
		// callers can wait without polling via MCPInitDone().
		done := make(chan struct{})
		gw.mcpInitDone = done
		go func() {
			defer close(done)
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			reg.InitializeAll(ctx, func(name string, initErr error) {
				slog.Error("mcp: server initialization failed",
					"server", name,
					"error", initErr,
				)
			})
		}()
	}

	return gw, nil
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

	roleUser = "user"
)

// RegisterProvider registers a provider with the gateway.
func (g *Gateway) RegisterProvider(p providers.Provider) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, exists := g.providers[p.Name()]; !exists {
		g.providerNames = append(g.providerNames, p.Name())
	}
	g.providers[p.Name()] = p
	g.rebuildModelIndexesLocked()
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
// invoked for every event on the shared bounded hook worker pool, so hook
// implementations should return promptly and avoid indefinite blocking.
func (g *Gateway) AddHook(fn EventHookFunc) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.hooks = append(g.hooks, fn)
	g.hookSnapshot.Store(append([]EventHookFunc(nil), g.hooks...))
}

func (g *Gateway) hasHooks() bool {
	return len(g.currentHooks()) > 0
}

// MCPInitDone returns a channel that is closed once the background MCP
// initialization goroutine has finished (successfully or not). When no MCP
// servers are configured a pre-closed channel is returned so callers can
// always safely select on the result.
//
//	example:
//	    select {
//	    case <-gw.MCPInitDone():
//	    case <-ctx.Done():
//	    }
//
// buildMCPAuditFn converts the public ToolCallAuditFn into the internal
// mcp.AuditFn expected by the Executor.  Returns nil when fn is nil so the
// Executor skips audit logging entirely.
func buildMCPAuditFn(fn pubmcp.ToolCallAuditFn) mcp.AuditFn {
	if fn == nil {
		return nil
	}
	return func(ctx context.Context, serverName, toolName, status string, latencyMs int, errMsg string) {
		fn(ctx, pubmcp.ToolCallAuditEntry{
			ServerName:   serverName,
			ToolName:     toolName,
			Status:       status,
			LatencyMs:    latencyMs,
			ErrorMessage: errMsg,
		})
	}
}

// MCPInitDone returns a channel that is closed once all MCP servers have
// completed their initialization handshake.  The channel is pre-closed when
// no MCP servers are configured.
func (g *Gateway) MCPInitDone() <-chan struct{} {
	g.mu.RLock()
	done := g.mcpInitDone
	g.mu.RUnlock()
	if done == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return done
}

// Route routes a request to the appropriate provider based on the configuration.
func (g *Gateway) Route(ctx context.Context, req providers.Request) (*providers.Response, error) {
	ctx, task := trace.NewTask(ctx, "gateway.route")
	defer task.End()

	start := time.Now()
	hooksEnabled := g.hasHooks()

	// Resolve model alias before routing.
	trace.WithRegion(ctx, "gateway.route.resolve_alias", func() {
		req = g.resolveAlias(req)
	})

	s, err := g.getStrategy()
	if err != nil {
		return nil, err
	}

	// Run before-request plugins (guardrails, transforms, rate-limit).
	var pctx *plugin.Context
	if g.plugins.HasPlugins() {
		pctx = plugin.NewContext(&req)
		trace.WithRegion(ctx, "gateway.route.plugins.before", func() {
			err = g.plugins.RunBefore(ctx, pctx)
		})
		if err != nil {
			metrics.ForRequest("", req.Model).Rejected.Inc()
			return nil, err
		}
		// Propagate any request mutations made by before-request plugins.
		if pctx.Request != nil {
			req = *pctx.Request
		}
	}

	// Inject MCP tool definitions into the request when servers are ready.
	var mcpTools []mcp.Tool
	if g.mcpRegistry != nil {
		mcpTools = g.mcpRegistry.AllTools()
	}
	if len(mcpTools) > 0 {
		// Build a set of tool names already present in the request so we do not
		// inject duplicate definitions when the caller has pre-populated Tools.
		existing := make(map[string]struct{}, len(req.Tools))
		for _, t := range req.Tools {
			existing[t.Function.Name] = struct{}{}
		}
		for _, t := range mcpTools {
			if _, dup := existing[t.Name]; dup {
				continue
			}
			req.Tools = append(req.Tools, core.Tool{
				Type: "function",
				Function: core.Function{
					Name:        t.Name,
					Description: t.Description,
					Parameters:  t.InputSchema,
				},
			})
		}
	}

	// During the agentic loop intermediate calls must be non-streaming so the
	// full response can be inspected for tool_calls. The client's original
	// stream preference is restored on the final response (Phase 1: always
	// returns non-streaming for MCP requests).
	originalStream := req.Stream
	if len(mcpTools) > 0 {
		req.Stream = false
	}

	// Execute the strategy (provider selection + actual call).
	var resp *providers.Response
	trace.WithRegion(ctx, "gateway.route.provider.execute", func() {
		resp, err = s.Execute(ctx, req)
	})
	latency := time.Since(start)

	if err != nil {
		if pctx != nil {
			pctx.Error = err
			g.plugins.RunOnError(ctx, pctx)
		}

		provider := ""
		errType := "provider_error"
		if errors.Is(err, circuitbreaker.ErrCircuitOpen) {
			errType = "circuit_open"
		}
		metrics.ForRequest("", req.Model).Error.Inc()
		metrics.ForProviderError(provider, errType).Inc()

		logging.FromContext(ctx).Error("request failed",
			"model", req.Model,
			"latency_ms", latency.Milliseconds(),
			"error", err.Error(),
		)

		if hooksEnabled {
			g.publishEvent(ctx, failedEventData(
				logging.TraceIDFromContext(ctx),
				"",
				req.Model,
				err.Error(),
				latency,
				originalStream,
			))
		}
		return nil, err
	}

	// Ensure OpenAI-compatible envelope fields are always set.
	if resp.Object == "" {
		resp.Object = "chat.completion"
	}
	if resp.Created == 0 {
		resp.Created = time.Now().Unix()
	}

	// Record latency for the least-latency routing strategy.
	if resp.Provider != "" {
		g.latencyTracker.Record(resp.Provider, latency)
	}

	// Agentic MCP tool-call loop. Runs only when MCP is active and the LLM
	// returned tool_calls. Each iteration executes the tools and re-contacts
	// the LLM until no more tool_calls are present or the depth limit is hit.
	if g.mcpExecutor != nil && len(mcpTools) > 0 {
		depth := 0
		trace.WithRegion(ctx, "gateway.route.mcp.loop", func() {
			for g.mcpExecutor.ShouldContinueLoop(resp, depth) {
				depth++

				// ResolvePendingToolCalls returns the assistant message (with tool_calls)
				// plus one tool-result message per call — append all at once.
				toolMsgs, toolErr := g.mcpExecutor.ResolvePendingToolCalls(ctx, resp)
				if toolErr != nil {
					err = fmt.Errorf("mcp tool execution at depth %d: %w", depth, toolErr)
					return
				}
				req.Messages = append(req.Messages, toolMsgs...)

				// Always non-streaming for intermediate calls.
				req.Stream = false

				resp, err = s.Execute(ctx, req)
				if err != nil {
					return
				}
			}
		})
		if err != nil {
			return nil, err
		}
	}
	// originalStream is included in the completed event so hook consumers
	// can distinguish streaming vs non-streaming requests (Phase 1.5 note:
	// when final-response streaming lands, remove the force-to-false above).

	// Run after-request plugins (logging, caching).
	if pctx != nil {
		pctx.Response = resp
		trace.WithRegion(ctx, "gateway.route.plugins.after", func() {
			err = g.plugins.RunAfter(ctx, pctx)
		})
		if err != nil {
			metrics.ForRequest(resp.Provider, resp.Model).Rejected.Inc()
			return nil, err
		}
		if pctx.Response != nil {
			resp = pctx.Response
		}
	}

	// Emit Prometheus metrics.
	requestMetrics := metrics.ForRequest(resp.Provider, resp.Model)
	requestMetrics.Duration.Observe(latency.Seconds())
	requestMetrics.Success.Inc()
	requestMetrics.TokensIn.Add(float64(resp.Usage.PromptTokens))
	requestMetrics.TokensOut.Add(float64(resp.Usage.CompletionTokens))

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
		requestMetrics.CostUSD.Add(cost.TotalUSD)
	}

	if logging.Enabled(ctx, slog.LevelDebug) {
		logging.FromContext(ctx).Debug("request completed",
			"model", resp.Model,
			"provider", resp.Provider,
			"latency_ms", latency.Milliseconds(),
			"tokens_in", resp.Usage.PromptTokens,
			"tokens_out", resp.Usage.CompletionTokens,
			"cost_usd", cost.TotalUSD,
		)
	}

	if hooksEnabled {
		g.publishEvent(ctx, completedEventData(
			logging.TraceIDFromContext(ctx),
			resp.Provider,
			resp.Model,
			latency,
			originalStream,
			resp.Usage.PromptTokens,
			resp.Usage.CompletionTokens,
			cost,
		))
	}

	return resp, nil
}

// publishEvent calls all registered hooks asynchronously.
func (g *Gateway) publishEvent(ctx context.Context, event events.HookEvent) {
	hooks := g.currentHooks()
	if len(hooks) == 0 {
		return
	}

	for _, hook := range hooks {
		dispatch := hookDispatch{
			ctx:   ctx,
			event: event,
			hook:  hook,
		}

		select {
		case g.hookDispatchQ <- dispatch:
		default:
			// Queue full — drop hook dispatches to avoid unbounded goroutine creation.
			metrics.HookEventsDroppedTotal.WithLabelValues(event.Subject).Inc()
		}
	}
}

func (g *Gateway) currentHooks() []EventHookFunc {
	hooks, _ := g.hookSnapshot.Load().([]EventHookFunc)
	return hooks
}

func (g *Gateway) startHookWorkers() {
	workerCount := runtime.GOMAXPROCS(0)
	if workerCount < 1 {
		workerCount = 1
	}
	if workerCount > 4 {
		workerCount = 4
	}

	for range workerCount {
		go func() {
			for dispatch := range g.hookDispatchQ {
				runHookDispatch(dispatch)
			}
		}()
	}
}

func runHookDispatch(dispatch hookDispatch) {
	data := dispatch.event.Map()
	defer func() {
		if r := recover(); r != nil {
			logging.Logger.Error("event hook panicked",
				"subject", dispatch.event.Subject,
				"panic", r,
			)
		}
	}()
	dispatch.hook(dispatch.ctx, dispatch.event.Subject, data)
}

func failedEventData(traceID, provider, model, errMsg string, latency time.Duration, stream bool) events.HookEvent {
	return events.FailedRequest(traceID, provider, model, errMsg, latency, stream)
}

func completedEventData(traceID, provider, model string, latency time.Duration, stream bool, tokensIn, tokensOut int, cost models.CostResult) events.HookEvent {
	return events.CompletedRequest(traceID, provider, model, latency, stream, tokensIn, tokensOut, cost, true)
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

	// Re-register MCP servers from the new config.
	if len(cfg.MCPServers) > 0 {
		reg := mcp.NewRegistry()
		for _, mcpCfg := range cfg.MCPServers {
			reg.RegisterConfig(mcpCfg)
		}
		maxDepth := 0
		for _, mcpCfg := range cfg.MCPServers {
			if mcpCfg.MaxCallDepth > 0 && (maxDepth == 0 || mcpCfg.MaxCallDepth < maxDepth) {
				maxDepth = mcpCfg.MaxCallDepth
			}
		}
		g.mcpRegistry = reg
		g.mcpExecutor = mcp.NewExecutor(reg, maxDepth, buildMCPAuditFn(cfg.MCPToolCallAuditFn))
		done := make(chan struct{})
		g.mcpInitDone = done
		go func() {
			defer close(done)
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			reg.InitializeAll(ctx, func(name string, initErr error) {
				slog.Error("mcp: server initialization failed after reload",
					"server", name,
					"error", initErr,
				)
			})
		}()
	} else {
		g.mcpRegistry = nil
		g.mcpExecutor = nil
		g.mcpInitDone = nil
	}

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
		for _, t := range g.config.Targets {
			if t.Retry == nil {
				continue
			}
			fb.WithTargetRetry(t.VirtualKey, t.Retry.Attempts, t.Retry.OnStatusCodes, t.Retry.InitialBackoffMs)
		}
		s = fb
	case ModeLoadBalance:
		s = strategies.NewLoadBalance(targets, lookup)
	case ModeLatency:
		if len(targets) == 0 {
			return nil, fmt.Errorf("no targets configured for least-latency strategy")
		}
		s = strategies.NewLeastLatency(targets, lookup, g.latencyTracker)
	case ModeCostOptimized:
		if len(targets) == 0 {
			return nil, fmt.Errorf("no targets configured for cost-optimized strategy")
		}
		s = strategies.NewCostOptimized(targets, lookup, g.catalog)
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
	case ModeContentBased:
		cbs, err := g.buildContentBasedStrategy(targets, lookup)
		if err != nil {
			return nil, err
		}
		s = cbs
	case ModeABTest:
		abt, err := g.buildABTestStrategy(lookup)
		if err != nil {
			return nil, err
		}
		s = abt
	default:
		return nil, fmt.Errorf("unknown strategy mode: %s", g.config.Strategy.Mode)
	}

	g.strategy = s
	return s, nil
}

// buildContentBasedStrategy constructs a ContentBased strategy from the gateway config.
func (g *Gateway) buildContentBasedStrategy(targets []strategies.Target, lookup strategies.ProviderLookup) (strategies.Strategy, error) {
	if len(g.config.Strategy.ContentConditions) == 0 {
		return nil, fmt.Errorf("no content_conditions configured for content-based strategy")
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("no targets configured for content-based strategy")
	}
	var rules []strategies.ContentRule
	for _, cc := range g.config.Strategy.ContentConditions {
		rules = append(rules, strategies.ContentRule{
			Type:   strategies.ContentConditionType(cc.Type),
			Value:  cc.Value,
			Target: strategies.Target{VirtualKey: cc.TargetKey},
		})
	}
	return strategies.NewContentBased(rules, targets[0], lookup)
}

// buildABTestStrategy constructs an ABTest strategy from the gateway config.
func (g *Gateway) buildABTestStrategy(lookup strategies.ProviderLookup) (strategies.Strategy, error) {
	if len(g.config.Strategy.ABVariants) == 0 {
		return nil, fmt.Errorf("no ab_variants configured for ab-test strategy")
	}
	var variants []strategies.ABTestVariant
	for _, v := range g.config.Strategy.ABVariants {
		variants = append(variants, strategies.ABTestVariant{
			Target: strategies.Target{VirtualKey: v.TargetKey},
			Weight: v.Weight,
			Label:  v.Label,
		})
	}
	return strategies.NewABTest(variants, lookup)
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

// RouteStream runs before-request plugins then returns a metered streaming
// response channel. Provider resolution follows the configured strategy mode,
// then falls back to any registered provider that supports the requested model
// and streaming. Prometheus metrics and event hooks are emitted when the
// returned channel drains (matching the behaviour of Route for non-streaming).
//
// When MCP servers are configured the request is routed through Route instead
// so that the full agentic tool-call loop can run. The final response is
// wrapped into a single-chunk stream and returned to the caller (Phase 1
// behaviour — true final-response streaming is Phase 1.5).
func (g *Gateway) RouteStream(ctx context.Context, req providers.Request) (<-chan providers.StreamChunk, error) {
	ctx, task := trace.NewTask(ctx, "gateway.route_stream")
	defer task.End()

	start := time.Now()
	hooksEnabled := g.hasHooks()
	var err error

	// Resolve model alias before routing.
	trace.WithRegion(ctx, "gateway.route_stream.resolve_alias", func() {
		req = g.resolveAlias(req)
	})

	// MCP redirect: when tool servers are registered, the agentic loop must
	// run to completion before any response is sent. Route() handles this
	// entirely; we wrap its non-streaming result into a channel here.
	g.mu.RLock()
	hasMCP := g.mcpRegistry != nil && g.mcpRegistry.HasServers()
	g.mu.RUnlock()
	if hasMCP {
		// Do not force req.Stream = false here: let Route() capture the
		// original stream flag via its own originalStream variable so that
		// emitted events correctly reflect stream: true for RouteStream callers.
		resp, err := g.Route(ctx, req)
		if err != nil {
			return nil, err
		}
		// Convert the completed Response into a buffered single-chunk channel.
		// Preserve all choices so n>1 requests are handled correctly, and use
		// the real FinishReason from each choice rather than hardcoding "stop".
		ch := make(chan providers.StreamChunk, 1)
		streamChoices := make([]providers.StreamChoice, len(resp.Choices))
		for i, c := range resp.Choices {
			streamChoices[i] = providers.StreamChoice{
				Index: c.Index,
				Delta: providers.MessageDelta{
					Role:      c.Message.Role,
					Content:   c.Message.Content,
					ToolCalls: c.Message.ToolCalls,
				},
				FinishReason: c.FinishReason,
			}
		}
		ch <- providers.StreamChunk{
			ID:      resp.ID,
			Object:  "chat.completion.chunk",
			Created: resp.Created,
			Model:   resp.Model,
			Choices: streamChoices,
			Usage:   &resp.Usage,
		}
		close(ch)
		_ = start // latency already recorded inside Route()
		return ch, nil
	}

	// Run before-request plugins (word-filter, max-token, rate-limit, etc.).
	pctx := plugin.NewContext(&req)
	if g.plugins.HasPlugins() {
		trace.WithRegion(ctx, "gateway.route_stream.plugins.before", func() {
			err = g.plugins.RunBefore(ctx, pctx)
		})
		if err != nil {
			metrics.ForRequest("", req.Model).Rejected.Inc()
			return nil, err
		}
		if pctx.Reject {
			metrics.ForRequest("", req.Model).Rejected.Inc()
			return nil, fmt.Errorf("request rejected by plugin: %s", pctx.Reason)
		}
	}
	// Propagate any modifications made by plugins (e.g., capped max_tokens).
	if pctx.Request != nil {
		req = *pctx.Request
	}

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
		if name, fallback, ok := g.findStreamingProviderMatchByModelLocked(req.Model); ok {
			sp = fallback
			if cb, hasCB := g.circuitBreakers[name]; hasCB {
				sp = &cbProvider{Provider: g.providers[name], cb: cb, name: name}
			}
		}
	}
	g.mu.RUnlock()

	if sp == nil {
		return nil, fmt.Errorf("no streaming-capable provider found for model: %s", req.Model)
	}

	providerName := sp.Name()
	if logging.Enabled(ctx, slog.LevelDebug) {
		logging.FromContext(ctx).Debug("stream request started", "model", req.Model, "provider", providerName)
	}

	var rawCh <-chan providers.StreamChunk
	trace.WithRegion(ctx, "gateway.route_stream.provider.start", func() {
		rawCh, err = sp.CompleteStream(ctx, req)
	})
	if err != nil {
		errType := "provider_error"
		if errors.Is(err, circuitbreaker.ErrCircuitOpen) {
			errType = "circuit_open"
		}
		metrics.ForRequest(providerName, req.Model).Error.Inc()
		metrics.ForProviderError(providerName, errType).Inc()
		if hooksEnabled {
			g.publishEvent(ctx, failedEventData(
				logging.TraceIDFromContext(ctx),
				providerName,
				req.Model,
				err.Error(),
				time.Since(start),
				true,
			))
		}
		return nil, err
	}

	// Wrap the raw channel with a metering goroutine that emits Prometheus
	// metrics and event hooks once the stream completes.
	g.mu.RLock()
	catalog := g.catalog
	g.mu.RUnlock()

	meta := streamwrap.MeterMeta{
		Provider: providerName,
		Model:    req.Model,
		Catalog:  catalog,
		TraceID:  logging.TraceIDFromContext(ctx),
	}
	if hooksEnabled {
		meta.PublishFn = g.publishEvent
	}
	return streamwrap.Meter(ctx, rawCh, start, meta), nil
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
	case ModeContentBased:
		// Evaluate content rules in order; first match wins, fallback is targets[0].
		for _, cond := range g.config.Strategy.ContentConditions {
			if streamingContentConditionMatches(cond, req) {
				// Matched target first, then remaining targets as fallback.
				keys := []string{cond.TargetKey}
				for _, t := range targets {
					keys = appendUniqueKey(keys, t.VirtualKey)
				}
				return keys
			}
		}
		// No rule matched — use declared target order (targets[0] is the fallback).
		keys := make([]string, 0, len(targets))
		for _, t := range targets {
			keys = append(keys, t.VirtualKey)
		}
		return keys
	case ModeABTest:
		// Weighted random variant selection mirrors ABTest.selectVariant.
		total := 0.0
		for _, v := range g.config.Strategy.ABVariants {
			w := v.Weight
			if w <= 0 {
				w = 1
			}
			total += w
		}
		if total > 0 {
			r := rand.Float64() * total //nolint:gosec
			cumulative := 0.0
			for _, v := range g.config.Strategy.ABVariants {
				w := v.Weight
				if w <= 0 {
					w = 1
				}
				cumulative += w
				if r < cumulative {
					keys := []string{v.TargetKey}
					for _, t := range targets {
						keys = appendUniqueKey(keys, t.VirtualKey)
					}
					return keys
				}
			}
			// Floating-point safety net — use last variant.
			last := g.config.Strategy.ABVariants[len(g.config.Strategy.ABVariants)-1]
			keys := []string{last.TargetKey}
			for _, t := range targets {
				keys = appendUniqueKey(keys, t.VirtualKey)
			}
			return keys
		}
		// No variants configured — fall through to raw order.
		keys := make([]string, 0, len(targets))
		for _, t := range targets {
			keys = append(keys, t.VirtualKey)
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

func (g *Gateway) rebuildModelIndexesLocked() {
	g.modelIndex.exactProviders = make(map[string][]string)
	g.modelIndex.exactStreamProviders = make(map[string][]string)
	g.modelIndex.exactEmbedProviders = make(map[string][]string)
	g.modelIndex.exactImageProviders = make(map[string][]string)

	for _, name := range g.providerNames {
		p, ok := g.providers[name]
		if !ok {
			continue
		}
		models := p.SupportedModels()
		for _, model := range models {
			g.modelIndex.exactProviders[model] = append(g.modelIndex.exactProviders[model], name)
		}
		if _, ok := p.(providers.StreamProvider); ok {
			for _, model := range models {
				g.modelIndex.exactStreamProviders[model] = append(g.modelIndex.exactStreamProviders[model], name)
			}
		}
		if _, ok := p.(providers.EmbeddingProvider); ok {
			for _, model := range models {
				g.modelIndex.exactEmbedProviders[model] = append(g.modelIndex.exactEmbedProviders[model], name)
			}
		}
		if _, ok := p.(providers.ImageProvider); ok {
			for _, model := range models {
				g.modelIndex.exactImageProviders[model] = append(g.modelIndex.exactImageProviders[model], name)
			}
		}
	}
}

func (g *Gateway) findProviderByModelLocked(model string) (providers.Provider, bool) {
	if exact := g.modelIndex.exactProviders[model]; len(exact) > 0 {
		return g.providers[exact[0]], true
	}

	for _, name := range g.providerNames {
		p, ok := g.providers[name]
		if ok && p.SupportsModel(model) {
			return p, true
		}
	}

	return nil, false
}

func (g *Gateway) findStreamingProviderMatchByModelLocked(model string) (string, providers.StreamProvider, bool) {
	if exact := g.modelIndex.exactStreamProviders[model]; len(exact) > 0 {
		name := exact[0]
		sp, _ := g.providers[name].(providers.StreamProvider)
		return name, sp, true
	}

	for _, name := range g.providerNames {
		p, ok := g.providers[name]
		if !ok || !p.SupportsModel(model) {
			continue
		}
		sp, ok := p.(providers.StreamProvider)
		if ok {
			return name, sp, true
		}
	}

	return "", nil, false
}

func (g *Gateway) findStreamingProviderByModelLocked(model string) (providers.StreamProvider, bool) {
	_, sp, ok := g.findStreamingProviderMatchByModelLocked(model)
	return sp, ok
}

func (g *Gateway) findEmbeddingProviderByModelLocked(model string) (providers.EmbeddingProvider, bool) {
	if exact := g.modelIndex.exactEmbedProviders[model]; len(exact) > 0 {
		ep, _ := g.providers[exact[0]].(providers.EmbeddingProvider)
		return ep, true
	}

	for _, name := range g.providerNames {
		p, ok := g.providers[name]
		if !ok || !p.SupportsModel(model) {
			continue
		}
		ep, ok := p.(providers.EmbeddingProvider)
		if ok {
			return ep, true
		}
	}

	return nil, false
}

func (g *Gateway) findImageProviderByModelLocked(model string) (providers.ImageProvider, bool) {
	if exact := g.modelIndex.exactImageProviders[model]; len(exact) > 0 {
		ip, _ := g.providers[exact[0]].(providers.ImageProvider)
		return ip, true
	}

	for _, name := range g.providerNames {
		p, ok := g.providers[name]
		if !ok || !p.SupportsModel(model) {
			continue
		}
		ip, ok := p.(providers.ImageProvider)
		if ok {
			return ip, true
		}
	}

	return nil, false
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

// streamingContentConditionMatches evaluates a single ContentCondition against
// a request, mirroring the logic in internal/strategies/contentbased.go.
func streamingContentConditionMatches(cond ContentCondition, req providers.Request) bool {
	switch cond.Type {
	case "prompt_contains":
		lower := strings.ToLower(cond.Value)
		for _, msg := range req.Messages {
			if msg.Role == roleUser && strings.Contains(strings.ToLower(msg.Content), lower) {
				return true
			}
		}
		return false
	case "prompt_not_contains":
		lower := strings.ToLower(cond.Value)
		for _, msg := range req.Messages {
			if msg.Role == roleUser && strings.Contains(strings.ToLower(msg.Content), lower) {
				return false
			}
		}
		return true
	case "prompt_regex":
		re, err := regexp.Compile(cond.Value)
		if err != nil {
			return false
		}
		for _, msg := range req.Messages {
			if msg.Role == roleUser && re.MatchString(msg.Content) {
				return true
			}
		}
		return false
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
	for _, name := range g.providerNames {
		p, ok := g.providers[name]
		if !ok {
			continue
		}
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
	names := make([]string, len(g.providerNames))
	copy(names, g.providerNames)
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
	return g.findProviderByModelLocked(model)
}

// FindStreamingByModel returns the first registered streaming-capable provider
// that supports the given model.
func (g *Gateway) FindStreamingByModel(model string) (providers.StreamProvider, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.findStreamingProviderByModelLocked(model)
}

// Close cleans up resources.
func (g *Gateway) Close() error {
	g.closeOnce.Do(func() {
		close(g.hookDispatchQ)
	})
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
	ep, ok := g.findEmbeddingProviderByModelLocked(req.Model)
	g.mu.RUnlock()

	if !ok {
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
	ip, ok := g.findImageProviderByModelLocked(req.Model)
	g.mu.RUnlock()

	if !ok {
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
