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
	"sort"
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
	"github.com/ferro-labs/ai-gateway/observability"
	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// EventHookFunc is called asynchronously after a gateway event (request
// completed or failed). It replaces the old EventPublisher interface with a
// simpler function-based hook pattern.
type EventHookFunc func(ctx context.Context, subject string, data map[string]any)

// Gateway is the main entry point for routing LLM requests.
type Gateway struct {
	mu                 sync.RWMutex
	config             Config
	catalog            models.Catalog
	providers          map[string]providers.Provider
	providerNames      []string
	strategy           strategies.Strategy
	streamingContent   []streamingContentCondition
	plugins            *plugin.Manager
	closeOnce          sync.Once
	hooks              []EventHookFunc
	hookSnapshot       atomic.Value
	hookDispatchQ      chan hookDispatch
	hookWorkersDone    sync.WaitGroup
	catalogRefreshDone sync.WaitGroup
	// shutdownCtx is a lifecycle context, not a request context. Storing it on the
	// struct is the intended idiom here: it is created once in New, parents the
	// gateway's background workers (hook dispatch, catalog refresh, MCP init), and
	// is cancelled by Close() to signal shutdown. It is never a per-request context.
	shutdownCtx      context.Context
	shutdownCancel   context.CancelFunc
	circuitBreakers  map[string]*circuitbreaker.CircuitBreaker
	discoveredModels map[string][]providers.ModelInfo
	latencyTracker   *latency.Tracker
	modelIndex       modelLookupIndex

	// obs is the observability provider used to emit per-request spans.
	// Defaults to observability.NoOp() when SetObservability has not
	// been called, which guarantees zero allocations on the hot path
	// (issue #49 acceptance criterion).
	obs observability.Provider

	// obsEventsActive is true when the installed Provider implements
	// observability.EventRecordingProvider and RecordingEnabled() returned
	// true at the time SetObservability was called.  It is read on the
	// hot path without holding the gateway mutex — it is set once before
	// traffic starts, so no additional synchronisation is required.
	obsEventsActive bool

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

type streamingContentCondition struct {
	ContentCondition
	re *regexp.Regexp
}

// hookDispatch is a work item handed to the async hook workers over a channel.
// Storing ctx in the struct is the documented exception to "don't store a context
// in a struct": the context travels *with* the work item to the goroutine that
// processes it, rather than outliving a call. See the Go blog's guidance on
// passing request-scoped values through a pipeline.
type hookDispatch struct {
	ctx   context.Context
	event events.HookEvent
	hook  EventHookFunc
}

const (
	hookDispatchQueueSize  = 256
	catalogRefreshInterval = 24 * time.Hour
)

// New creates a new Gateway instance with the given configuration.
func New(cfg Config) (*Gateway, error) {
	catalogResult, err := models.LoadWithInfo()
	recordCatalogLoad(catalogResult.Source, err)
	catalog := catalogResult.Catalog
	if err != nil {
		// Non-fatal: operate without model metadata (no enrichment / cost reporting).
		slog.Error("model catalog unavailable; continuing without catalog metadata", "url", catalogResult.URLForLog(), "error", err)
		catalog = models.Catalog{}
	}

	streamingContent, err := compileStreamingContentConditions(cfg.Strategy.Mode, cfg.Strategy.ContentConditions)
	if err != nil {
		return nil, err
	}

	gw := &Gateway{
		config:           cfg,
		catalog:          catalog,
		providers:        make(map[string]providers.Provider),
		streamingContent: streamingContent,
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
		obs:           observability.NoOp(),
	}
	gw.shutdownCtx, gw.shutdownCancel = context.WithCancel(context.Background()) //nolint:gosec // canceled by Gateway.Close()
	gw.hookSnapshot.Store([]EventHookFunc{})
	gw.startHookWorkers()
	gw.startCatalogRefresh()

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
			// Parent the init timeout on the gateway shutdown context so a slow
			// MCP handshake is cancelled by Close() instead of lingering up to
			// the full timeout after shutdown.
			ctx, cancel := context.WithTimeout(gw.shutdownCtx, 60*time.Second)
			defer cancel()
			reg.InitializeAll(ctx, func(name string, initErr error) {
				slog.Error("mcp: server initialization failed",
					"server", name,
					"error", initErr,
				)
			})
		}()
	}

	gw.mu.Lock()
	gw.ensureCircuitBreakersLocked()
	gw.mu.Unlock()

	return gw, nil
}

// SetObservability installs an observability.Provider on the gateway.
// Pass observability.NoOp() to disable. The provider's StartRequestSpan
// is called at the top of Route and RouteStream; span attributes are
// populated incrementally as the request progresses through routing,
// provider execution, plugins, and final cost/usage calculation.
//
// Safe to call only at startup, before serving traffic. The cmd/ferrogw
// wire-up constructs the provider via internal/otel.Init.
func (g *Gateway) SetObservability(p observability.Provider) {
	if p == nil {
		p = observability.NoOp()
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.obs = p
	// Cache whether the provider will receive RecordEvent calls so the
	// hot path can skip Event construction when nothing is listening.
	g.obsEventsActive = false
	if er, ok := p.(observability.EventRecordingProvider); ok {
		g.obsEventsActive = er.RecordingEnabled()
	}
}

// Observability returns the current observability.Provider. Always
// non-nil; defaults to NoOp.
func (g *Gateway) Observability() observability.Provider {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.obs
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

func (g *Gateway) startCatalogRefresh() {
	g.catalogRefreshDone.Add(1)
	go func() {
		defer g.catalogRefreshDone.Done()
		ticker := time.NewTicker(catalogRefreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-g.shutdownCtx.Done():
				return
			case <-ticker.C:
				g.refreshCatalog()
			}
		}
	}()
}

func (g *Gateway) refreshCatalog() {
	result, err := models.LoadWithInfo()
	recordCatalogLoad(result.Source, err)
	if err != nil {
		slog.Error("model catalog refresh failed", "url", result.URLForLog(), "error", err)
		return
	}
	if result.Source != models.LoadSourceRemote {
		slog.Warn("model catalog refresh skipped; keeping current catalog", "url", result.URLForLog(), "source", result.Source)
		return
	}

	g.mu.Lock()
	g.catalog = result.Catalog
	// The exact-match routing index is derived from the catalog (issue #146),
	// so it must be rebuilt whenever the catalog is replaced — otherwise the
	// 24h refresh would leave routing frozen at the startup catalog while
	// /v1/models reflects the new one.
	g.rebuildModelIndexesLocked()
	if g.config.Strategy.Mode == ModeCostOptimized {
		g.strategy = nil
	}
	g.mu.Unlock()

	slog.Info("model catalog refreshed", "url", result.URLForLog(), "models", len(result.Catalog))
}

func recordCatalogLoad(source models.LoadSource, err error) {
	if source == "" {
		source = models.LoadSourceFallback
	}
	result := "success"
	if err != nil {
		result = "error"
	}
	metrics.CatalogLoadsTotal.WithLabelValues(string(source), result).Inc()
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

// runBeforePlugins runs before-request plugins and returns an early response
// when a plugin (e.g. response-cache) sets Skip=true. It also propagates any
// request mutations the plugins made. RunAfter is called before returning the
// early response so logging/metrics plugins still fire.
func (g *Gateway) runBeforePlugins(ctx context.Context, plugins *plugin.Manager, pctx *plugin.Context, req *providers.Request) (*providers.Response, error) {
	if err := plugins.RunBefore(ctx, pctx); err != nil {
		return nil, err
	}
	if pctx.Request != nil {
		*req = *pctx.Request
	}
	if pctx.Skip && pctx.Response != nil {
		if err := plugins.RunAfter(ctx, pctx); err != nil {
			pctx.Error = err
			plugins.RunOnError(ctx, pctx)
			return nil, err
		}
		return pctx.Response, nil
	}
	return nil, nil
}

// Route routes a request to the appropriate provider based on the configuration.
func (g *Gateway) Route(ctx context.Context, req providers.Request) (*providers.Response, error) {
	ctx, task := trace.NewTask(ctx, "gateway.route")
	defer task.End()

	start := time.Now()
	hooksEnabled := g.hasHooks()
	req.NormalizeCompletionTokenLimits()

	// Start the observability root span. NoOp provider makes this a
	// zero-allocation call when tracing is disabled.
	g.mu.RLock()
	strategyMode := string(g.config.Strategy.Mode)
	obs := g.obs
	obsEventsActive := g.obsEventsActive
	mcpRegistrySnapshot := g.mcpRegistry
	mcpExecutorSnapshot := g.mcpExecutor
	plugins := g.plugins
	releasePlugins := acquirePluginManager(plugins)
	g.mu.RUnlock()
	defer releasePlugins()
	ctx, span := obs.StartRequestSpan(ctx, observability.RequestAttrs{
		Operation:       "chat",
		RequestModel:    req.Model,
		IsStream:        req.Stream,
		TraceID:         logging.TraceIDFromContext(ctx),
		RoutingStrategy: strategyMode,
	})
	defer span.End()

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
	if plugins.HasPlugins() {
		pctx = plugin.NewContext(&req)
		defer plugin.PutContext(pctx)
		var early *providers.Response
		trace.WithRegion(ctx, "gateway.route.plugins.before", func() {
			early, err = g.runBeforePlugins(ctx, plugins, pctx, &req)
		})
		if err != nil {
			metrics.ForRequest("", req.Model).Rejected.Inc()
			return nil, err
		}
		if early != nil {
			return early, nil
		}
	}

	// Inject MCP tool definitions into the request when servers are ready.
	var mcpTools []mcp.Tool
	if mcpRegistrySnapshot != nil {
		mcpTools = mcpRegistrySnapshot.AllTools()
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
	providerStart := time.Now()
	trace.WithRegion(ctx, "gateway.route.provider.execute", func() {
		resp, err = s.Execute(ctx, req)
	})
	providerDuration := time.Since(providerStart)
	latency := time.Since(start)

	if err != nil {
		if pctx != nil {
			pctx.Error = err
			plugins.RunOnError(ctx, pctx)
		}

		provider := ""
		errType := "provider_error"
		if errors.Is(err, circuitbreaker.ErrCircuitOpen) {
			errType = "circuit_open"
		}
		metrics.ForRequest("", req.Model).Error.Inc()
		metrics.ForProviderError(provider, errType).Inc()

		span.SetError(err)

		logging.FromContext(ctx).Error("request failed",
			"model", req.Model,
			"latency_ms", latency.Milliseconds(),
			"error", err.Error(),
		)

		if hooksEnabled || obsEventsActive {
			he := failedEventData(
				logging.TraceIDFromContext(ctx),
				"",
				req.Model,
				err.Error(),
				latency,
				originalStream,
			)
			g.dispatchRequestEvent(ctx, obs, hooksEnabled, obsEventsActive, he)
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
	if mcpExecutorSnapshot != nil && len(mcpTools) > 0 {
		depth := 0
		trace.WithRegion(ctx, "gateway.route.mcp.loop", func() {
			for mcpExecutorSnapshot.ShouldContinueLoop(resp, depth) {
				depth++

				// ResolvePendingToolCalls returns the assistant message (with tool_calls)
				// plus one tool-result message per call — append all at once.
				toolMsgs, toolErr := mcpExecutorSnapshot.ResolvePendingToolCalls(ctx, resp)
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
			err = plugins.RunAfter(ctx, pctx)
		})
		if err != nil {
			metrics.ForRequest(resp.Provider, resp.Model).Rejected.Inc()
			pctx.Error = err
			plugins.RunOnError(ctx, pctx)
			return nil, err
		}
		if pctx.Response != nil {
			resp = pctx.Response
		}
	}

	// Emit metrics + cost, stamp the span, and dispatch the completed event.
	g.recordSuccess(ctx, span, obs, resp, latency, originalStream, hooksEnabled, obsEventsActive)

	resp.OverheadMs = float64((latency - providerDuration).Microseconds()) / 1000.0

	return resp, nil
}

// dispatchRequestEvent fans a request lifecycle event out to the async hook
// workers and/or the observability provider, depending on which sinks are
// active. Centralising the branching keeps Route/RouteStream readable and
// keeps the two delivery paths in sync.
func (g *Gateway) dispatchRequestEvent(ctx context.Context, obs observability.Provider, hooksEnabled, obsEventsActive bool, he events.HookEvent) {
	if hooksEnabled {
		g.publishEvent(ctx, he)
	}
	if obsEventsActive {
		obs.RecordEvent(ctx, obsEventFromHook(he))
	}
}

// recordSuccess emits Prometheus + cost metrics, stamps the root span with the
// resolved provider/model/usage/cost, logs at debug level, and dispatches the
// completed lifecycle event. Extracted from Route to keep its cyclomatic
// complexity in check.
func (g *Gateway) recordSuccess(ctx context.Context, span observability.Span, obs observability.Provider, resp *providers.Response, latency time.Duration, originalStream, hooksEnabled, obsEventsActive bool) {
	requestMetrics := metrics.ForRequest(resp.Provider, resp.Model)
	requestMetrics.Duration.Observe(latency.Seconds())
	requestMetrics.Success.Inc()
	requestMetrics.TokensIn.Add(float64(resp.Usage.PromptTokens))
	requestMetrics.TokensOut.Add(float64(resp.Usage.CompletionTokens))

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

	// Stamp final usage + cost + resolved provider/model on the root span.
	span.SetAttribute(observability.AttrGenAISystem, resp.Provider)
	span.SetAttribute(observability.AttrGenAIResponseModel, resp.Model)
	// Stamp the resolved target key (virtual key = provider name in this routing layer).
	if resp.Provider != "" {
		span.SetAttribute(observability.AttrFerroRoutingTargetKey, resp.Provider)
	}
	span.SetTokens(resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.ReasoningTokens)
	span.SetCost(observability.CostBreakdown{
		TotalUSD:      cost.TotalUSD,
		InputUSD:      cost.InputUSD,
		OutputUSD:     cost.OutputUSD,
		CacheReadUSD:  cost.CacheReadUSD,
		CacheWriteUSD: cost.CacheWriteUSD,
		ReasoningUSD:  cost.ReasoningUSD,
		ModelFound:    cost.ModelFound,
	})

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

	if hooksEnabled || obsEventsActive {
		he := completedEventData(
			logging.TraceIDFromContext(ctx),
			resp.Provider,
			resp.Model,
			latency,
			originalStream,
			resp.Usage.PromptTokens,
			resp.Usage.CompletionTokens,
			cost,
		)
		g.dispatchRequestEvent(ctx, obs, hooksEnabled, obsEventsActive, he)
	}
}

// publishEvent calls all registered hooks asynchronously.
func (g *Gateway) publishEvent(ctx context.Context, event events.HookEvent) {
	hooks := g.currentHooks()
	if len(hooks) == 0 {
		return
	}

	// Detach from the request lifecycle: hooks are dispatched asynchronously
	// and usually run after the HTTP handler has returned and ctx is already
	// cancelled. WithoutCancel drops cancellation (so ctx-aware hook work like
	// DB writes / outbound calls is not dead-on-arrival) while preserving the
	// request's trace context and values. Worker shutdown is governed by
	// g.shutdownCtx, not this context.
	detachedCtx := context.WithoutCancel(ctx)

	for _, hook := range hooks {
		dispatch := hookDispatch{
			ctx:   detachedCtx,
			event: event,
			hook:  hook,
		}

		// Bias toward the shutdown check first so we never race a Close()
		// that has already cancelled. Once shutdownCtx is Done we drop the
		// event rather than risk a send on what used to be a closed channel
		// (we no longer close hookDispatchQ — workers exit via shutdownCtx).
		// The nil-shutdownCtx branch supports a handful of unit tests that
		// build Gateway literals directly without going through New().
		if g.shutdownCtx != nil {
			select {
			case <-g.shutdownCtx.Done():
				return
			default:
			}
			select {
			case g.hookDispatchQ <- dispatch:
			case <-g.shutdownCtx.Done():
				return
			default:
				metrics.HookEventsDroppedTotal.WithLabelValues(event.Subject).Inc()
			}
			continue
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
		g.hookWorkersDone.Add(1)
		go func() {
			defer g.hookWorkersDone.Done()
			for {
				select {
				case <-g.shutdownCtx.Done():
					// Best-effort drain anything queued before exiting so we
					// don't lose events that were already enqueued.
					for {
						select {
						case dispatch := <-g.hookDispatchQ:
							runHookDispatch(dispatch)
						default:
							return
						}
					}
				case dispatch := <-g.hookDispatchQ:
					runHookDispatch(dispatch)
				}
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

// obsEventFromHook converts an internal HookEvent into the public
// observability.Event that is broadcast to plugin Exporters via
// Provider.RecordEvent. No prompt or response content is included —
// only request metadata and usage/cost numbers.
func obsEventFromHook(e events.HookEvent) observability.Event {
	return observability.Event{
		Subject:   e.Subject,
		TraceID:   e.TraceID,
		Provider:  e.Provider,
		Model:     e.Model,
		Status:    e.Status,
		Error:     e.Error,
		LatencyMs: e.LatencyMs,
		Stream:    e.Stream,
		TokensIn:  e.TokensIn,
		TokensOut: e.TokensOut,
		Cost: observability.CostBreakdown{
			TotalUSD:      e.Cost.TotalUSD,
			InputUSD:      e.Cost.InputUSD,
			OutputUSD:     e.Cost.OutputUSD,
			CacheReadUSD:  e.Cost.CacheReadUSD,
			CacheWriteUSD: e.Cost.CacheWriteUSD,
			ReasoningUSD:  e.Cost.ReasoningUSD,
			ModelFound:    e.Cost.ModelFound,
		},
		Timestamp: e.Timestamp,
	}
}

// ReloadConfig validates and applies a new configuration, forcing strategy rebuild on next request.
//
// The context satisfies the admin ConfigManager seam; the in-memory reload below
// performs no request-scoped store/IO of its own (MCP re-initialization is
// parented on the gateway shutdown context), so ctx is accepted but not used here.
func (g *Gateway) ReloadConfig(ctx context.Context, cfg Config) error {
	_ = ctx
	if err := ValidateConfig(cfg); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	streamingContent, err := compileStreamingContentConditions(cfg.Strategy.Mode, cfg.Strategy.ContentConditions)
	if err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	plugins, err := buildPluginManager(cfg.Plugins)
	if err != nil {
		return err
	}
	pluginsInstalled := false
	defer func() {
		if pluginsInstalled {
			return
		}
		if closeErr := closePluginManager(plugins); closeErr != nil {
			slog.Warn("plugin close failed after config reload error", "error", closeErr)
		}
	}()
	g.mu.Lock()
	oldPlugins := g.plugins
	g.config = cfg
	g.streamingContent = streamingContent
	g.plugins = plugins
	pluginsInstalled = true
	g.strategy = nil // force rebuild on next request
	g.circuitBreakers = make(map[string]*circuitbreaker.CircuitBreaker)
	g.ensureCircuitBreakersLocked()

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
			// Parent the init timeout on the gateway shutdown context so a slow
			// MCP handshake is cancelled by Close() instead of lingering up to
			// the full timeout after shutdown.
			ctx, cancel := context.WithTimeout(g.shutdownCtx, 60*time.Second)
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

	g.mu.Unlock()
	if err := closePluginManager(oldPlugins); err != nil {
		slog.Warn("plugin close failed during config reload", "error", err)
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

	g.ensureCircuitBreakersLocked()

	// Snapshot both maps under the write lock already held. The lookup closure
	// runs inside Strategy.Execute with no lock held, so capturing local copies
	// here is the only safe access pattern.
	// maps.Clone is a shallow copy — safe because map values (Provider, *CB) are
	// themselves immutable references; we never mutate through them in the closure.
	providerSnap := maps.Clone(g.providers)
	cbSnap := maps.Clone(g.circuitBreakers)

	// Provider lookup with transparent circuit-breaker wrapping.
	//
	// The closure is captured into the strategy and invoked later from the
	// request hot path, AFTER Route/RouteStream have released g.mu. It reads
	// from the snapshots captured above, so no lock is needed in the closure.
	lookup := func(name string) (providers.Provider, bool) {
		p, ok := providerSnap[name]
		if !ok {
			return nil, false
		}
		if cb, hasCB := cbSnap[name]; hasCB {
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
		s = strategies.NewCostOptimized(targets, lookup, g.catalog, g.config.Strategy.UnpricedStrategy)
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
		if shouldRecordCircuitBreakerFailure(ctx, err) {
			p.cb.RecordFailure()
			metrics.CircuitBreakerState.WithLabelValues(p.name).Set(float64(p.cb.State()))
		} else {
			p.cb.ReleaseProbe()
		}
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
		p.cb.ReleaseProbe()
		return nil, fmt.Errorf("provider %s does not support streaming", p.name)
	}
	ch, err := sp.CompleteStream(ctx, req)
	if err != nil {
		if shouldRecordCircuitBreakerFailure(ctx, err) {
			p.cb.RecordFailure()
			metrics.CircuitBreakerState.WithLabelValues(p.name).Set(float64(p.cb.State()))
		} else {
			p.cb.ReleaseProbe()
		}
		return nil, err
	}
	return ch, nil
}

// shouldRecordCircuitBreakerFailure reports whether an error should count toward
// opening the circuit. Caller-side cancellation/deadlines and rate limits are
// excluded so transient client behavior does not block healthy traffic.
// Provider-side timeouts that surface as context.DeadlineExceeded while the
// request context is still active are counted as failures.
func shouldRecordCircuitBreakerFailure(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		return false
	}
	return !isRateLimitError(err)
}

// recordStreamCircuitBreakerOutcome updates breaker state when a stream
// finishes. Startup failures are recorded in cbProvider.CompleteStream;
// this handles stream completion only.
func recordStreamCircuitBreakerOutcome(ctx context.Context, cb *circuitbreaker.CircuitBreaker, name string, err error) {
	if err != nil {
		if !shouldRecordCircuitBreakerFailure(ctx, err) {
			cb.ReleaseProbe()
			return
		}
		cb.RecordFailure()
		metrics.CircuitBreakerState.WithLabelValues(name).Set(float64(cb.State()))
		return
	}
	cb.RecordSuccess()
	metrics.CircuitBreakerState.WithLabelValues(name).Set(0)
}

// ensureCircuitBreakersLocked creates circuit breakers for configured targets.
// Caller must hold g.mu.
func (g *Gateway) ensureCircuitBreakersLocked() {
	for _, t := range g.config.Targets {
		if t.CircuitBreaker == nil {
			continue
		}
		if _, exists := g.circuitBreakers[t.VirtualKey]; exists {
			continue
		}
		timeout, _ := time.ParseDuration(t.CircuitBreaker.Timeout)
		g.circuitBreakers[t.VirtualKey] = circuitbreaker.New(
			t.CircuitBreaker.FailureThreshold,
			t.CircuitBreaker.SuccessThreshold,
			t.CircuitBreaker.MaxHalfThreshold,
			timeout,
		)
	}
}

// isRateLimitError checks if the error is a 429 rate limit response.
// Rate limits are expected and temporary — they should not trip the circuit breaker.
func isRateLimitError(err error) bool {
	return providers.ParseStatusCode(err) == 429
}

// LoadPlugins initializes and registers plugins from the gateway configuration.
func (g *Gateway) LoadPlugins() error {
	g.mu.RLock()
	configs := append([]PluginConfig(nil), g.config.Plugins...)
	g.mu.RUnlock()
	plugins, err := buildPluginManager(configs)
	if err != nil {
		return err
	}

	g.mu.Lock()
	oldPlugins := g.plugins
	g.plugins = plugins
	g.mu.Unlock()
	if err := closePluginManager(oldPlugins); err != nil {
		return err
	}
	return nil
}

func buildPluginManager(configs []PluginConfig) (*plugin.Manager, error) {
	plugins := plugin.NewManager()
	for _, pc := range configs {
		if !pc.Enabled {
			continue
		}
		factory, ok := plugin.GetFactory(pc.Name)
		if !ok {
			_ = plugins.Close()
			return nil, fmt.Errorf("unknown plugin: %s", pc.Name)
		}
		p := factory()
		if err := p.Init(pc.Config); err != nil {
			_ = plugins.Close()
			_ = p.Close()
			return nil, fmt.Errorf("plugin %s init failed: %w", pc.Name, err)
		}
		stage := plugin.Stage(pc.Stage)
		if err := plugins.Register(stage, p); err != nil {
			_ = plugins.Close()
			_ = p.Close()
			return nil, fmt.Errorf("plugin %s register failed: %w", pc.Name, err)
		}
	}
	return plugins, nil
}

func closePluginManager(plugins *plugin.Manager) error {
	if plugins == nil {
		return nil
	}
	return plugins.Close()
}

func acquirePluginManager(plugins *plugin.Manager) func() {
	if plugins == nil || !plugins.HasPlugins() {
		return func() {}
	}
	return plugins.Acquire()
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
	req.NormalizeCompletionTokenLimits()
	var err error

	// Start the observability root span. End() is normally called by
	// streamwrap.Meter when the stream drains (via the SpanFinisher
	// closure below). On the synchronous error paths below we end it
	// explicitly. streamEnded prevents a double-End.
	g.mu.RLock()
	strategyMode := string(g.config.Strategy.Mode)
	obs := g.obs
	obsEventsActive := g.obsEventsActive
	mcpRegistrySnapshot := g.mcpRegistry
	plugins := g.plugins
	releasePlugins := acquirePluginManager(plugins)
	g.mu.RUnlock()
	var releasePluginsOnce sync.Once
	releasePluginManager := func() {
		releasePluginsOnce.Do(releasePlugins)
	}
	ctx, span := obs.StartRequestSpan(ctx, observability.RequestAttrs{
		Operation:       "chat",
		RequestModel:    req.Model,
		IsStream:        true,
		TraceID:         logging.TraceIDFromContext(ctx),
		RoutingStrategy: strategyMode,
	})
	streamEnded := false
	defer func() {
		if !streamEnded {
			span.End()
		}
	}()

	// Resolve model alias before routing.
	trace.WithRegion(ctx, "gateway.route_stream.resolve_alias", func() {
		req = g.resolveAlias(req)
	})

	// MCP redirect: when tool servers are registered, the agentic loop must
	// run to completion before any response is sent. Route() handles this
	// entirely; we wrap its non-streaming result into a channel here.
	hasMCP := mcpRegistrySnapshot != nil && mcpRegistrySnapshot.HasServers()
	if hasMCP {
		releasePluginManager()
		// Do not force req.Stream = false here: let Route() capture the
		// original stream flag via its own originalStream variable so that
		// emitted events correctly reflect stream: true for RouteStream callers.
		resp, err := g.Route(ctx, req)
		if err != nil {
			return nil, err
		}
		_ = start // latency already recorded inside Route()
		return responseStream(resp), nil
	}

	// Run before-request plugins (word-filter, max-token, rate-limit, etc.).
	var pctx *plugin.Context
	if plugins.HasPlugins() {
		pctx = plugin.NewContext(&req)
		var early *providers.Response
		trace.WithRegion(ctx, "gateway.route_stream.plugins.before", func() {
			early, err = g.runBeforePlugins(ctx, plugins, pctx, &req)
		})
		if err != nil {
			plugin.PutContext(pctx)
			pctx = nil
			releasePluginManager()
			metrics.ForRequest("", req.Model).Rejected.Inc()
			return nil, err
		}
		if early != nil {
			plugin.PutContext(pctx)
			pctx = nil
			releasePluginManager()
			return responseStream(early), nil
		}
	} else {
		releasePluginManager()
	}

	// Resolve provider according to strategy mode.
	g.mu.Lock()
	g.ensureCircuitBreakersLocked()
	g.mu.Unlock()
	g.mu.RLock()
	sp, orderErr := g.resolveStreamingProviderLocked(req)
	g.mu.RUnlock()

	if orderErr != nil {
		err = orderErr
		span.SetError(err)
		if pctx != nil {
			pctx.Error = err
			plugins.RunOnError(ctx, pctx)
			plugin.PutContext(pctx)
			releasePluginManager()
		}
		return nil, err
	}

	if sp == nil {
		err = fmt.Errorf("no streaming-capable provider found for model: %s", req.Model)
		span.SetError(err)
		if pctx != nil {
			pctx.Error = err
			plugins.RunOnError(ctx, pctx)
			plugin.PutContext(pctx)
			releasePluginManager()
		}
		return nil, err
	}

	providerName := sp.Name()
	span.SetAttribute(observability.AttrGenAISystem, providerName)
	// Stamp the resolved target key (virtual key = provider name in this routing layer).
	if providerName != "" {
		span.SetAttribute(observability.AttrFerroRoutingTargetKey, providerName)
	}
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
		if pctx != nil {
			pctx.Error = err
			plugins.RunOnError(ctx, pctx)
			plugin.PutContext(pctx)
			releasePluginManager()
		}
		metrics.ForRequest(providerName, req.Model).Error.Inc()
		metrics.ForProviderError(providerName, errType).Inc()
		span.SetError(err)
		if hooksEnabled || obsEventsActive {
			he := failedEventData(
				logging.TraceIDFromContext(ctx),
				providerName,
				req.Model,
				err.Error(),
				time.Since(start),
				true,
			)
			g.dispatchRequestEvent(ctx, obs, hooksEnabled, obsEventsActive, he)
		}
		return nil, err
	}

	// Wrap the raw channel with a metering goroutine that emits Prometheus
	// metrics and event hooks once the stream completes.
	g.mu.RLock()
	catalog := g.catalog
	g.mu.RUnlock()

	meta := streamwrap.MeterMeta{
		Provider:        providerName,
		Model:           req.Model,
		Catalog:         catalog,
		TraceID:         logging.TraceIDFromContext(ctx),
		LatencyRecorder: g.latencyTracker.Record,
	}
	if hooksEnabled {
		meta.PublishFn = g.publishEvent
	}
	if wrapped, ok := sp.(*cbProvider); ok {
		cb := wrapped.cb
		cbName := wrapped.name
		meta.CircuitBreakerOutcome = func(err error) {
			recordStreamCircuitBreakerOutcome(ctx, cb, cbName, err)
		}
	}
	if pctx != nil {
		meta.CompletionFn = func(ctx context.Context, resp *providers.Response) error {
			pctx.Response = resp
			err := plugins.RunAfter(ctx, pctx)
			if pctx.Response != nil {
				*resp = *pctx.Response
			}
			if err != nil {
				pctx.Error = err
				plugins.RunOnError(ctx, pctx)
			}
			plugin.PutContext(pctx)
			pctx = nil
			releasePluginManager()
			return err
		}
		meta.ErrorFn = func(ctx context.Context, err error) {
			if pctx == nil {
				return
			}
			pctx.Error = err
			plugins.RunOnError(ctx, pctx)
			plugin.PutContext(pctx)
			pctx = nil
			releasePluginManager()
		}
	}

	// Hand the root span off to streamwrap so token, cost, and timing
	// attributes are stamped after the channel drains. The finisher
	// closes the span; the deferred fallback above is suppressed via
	// streamEnded.
	streamEnded = true
	finishSpan := span
	// obsProvider and obsEventsActive are the snapshot locals captured at the
	// top of RouteStream — they must not re-read g.obs / g.obsEventsActive here.
	obsProvider := obs
	traceID := logging.TraceIDFromContext(ctx)
	meta.SpanFinisher = streamwrap.SpanFinisherFunc(func(o streamwrap.StreamOutcome) {
		finishSpan.SetTokens(o.TokensIn, o.TokensOut, o.ReasoningIn)
		finishSpan.SetCost(observability.CostBreakdown{
			TotalUSD:      o.Cost.TotalUSD,
			InputUSD:      o.Cost.InputUSD,
			OutputUSD:     o.Cost.OutputUSD,
			CacheReadUSD:  o.Cost.CacheReadUSD,
			CacheWriteUSD: o.Cost.CacheWriteUSD,
			ReasoningUSD:  o.Cost.ReasoningUSD,
			ModelFound:    o.Cost.ModelFound,
		})
		finishSpan.SetStreamTimings(o.TTFTMs, o.TTLTMs)
		if o.ErrorMsg != "" {
			finishSpan.SetError(errors.New(o.ErrorMsg))
		}
		finishSpan.End()

		// Emit observability event for streaming completion/failure.
		if obsEventsActive {
			var he events.HookEvent
			if o.ErrorMsg != "" {
				he = events.FailedRequest(
					traceID,
					providerName,
					req.Model,
					o.ErrorMsg,
					time.Duration(o.TTLTMs*float64(time.Millisecond)),
					true,
				)
			} else {
				he = events.CompletedRequest(
					traceID,
					providerName,
					req.Model,
					time.Duration(o.TTLTMs*float64(time.Millisecond)),
					true,
					o.TokensIn,
					o.TokensOut,
					o.Cost,
					false,
				)
			}
			// Detach from the request lifecycle: this closure runs in the
			// streamwrap goroutine after the HTTP handler has returned and the
			// request ctx is already cancelled. WithoutCancel drops cancellation
			// while preserving the request's trace context, so the recorded
			// event stays linked to the originating trace.
			obsProvider.RecordEvent(context.WithoutCancel(ctx), obsEventFromHook(he))
		}
	})
	return streamwrap.Meter(ctx, rawCh, start, meta), nil
}

func responseStream(resp *providers.Response) <-chan providers.StreamChunk {
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
	return ch
}

func (g *Gateway) resolveStreamingProviderLocked(req providers.Request) (providers.StreamProvider, error) {
	orderedKeys, err := g.streamingTargetOrderLocked(req)
	if err != nil {
		return nil, err
	}
	var openCircuitTarget providers.StreamProvider
	for _, key := range orderedKeys {
		sp, ok := g.streamingProviderForTargetLocked(key, req.Model)
		if !ok {
			continue
		}
		if wrapped, isCB := sp.(*cbProvider); isCB && !wrapped.cb.Allow() {
			openCircuitTarget = sp
			continue
		}
		return sp, nil
	}
	if openCircuitTarget != nil {
		return openCircuitTarget, nil
	}

	// Fallback: any registered provider that supports this model and streaming.
	name, fallback, ok := g.findStreamingProviderMatchByModelLocked(req.Model)
	if !ok {
		return nil, nil
	}
	if cb, hasCB := g.circuitBreakers[name]; hasCB {
		return &cbProvider{Provider: g.providers[name], cb: cb, name: name}, nil
	}
	return fallback, nil
}

func (g *Gateway) streamingProviderForTargetLocked(key, model string) (providers.StreamProvider, bool) {
	p, ok := g.providers[key]
	if !ok || !p.SupportsModel(model) {
		return nil, false
	}

	sp, ok := p.(providers.StreamProvider)
	if !ok {
		return nil, false
	}

	// Apply circuit breaker if configured.
	if cb, hasCB := g.circuitBreakers[key]; hasCB {
		return &cbProvider{Provider: p, cb: cb, name: key}, true
	}
	return sp, true
}

func (g *Gateway) streamingTargetOrderLocked(req providers.Request) ([]string, error) {
	targets := g.config.Targets
	if len(targets) == 0 {
		return nil, nil
	}

	switch g.config.Strategy.Mode {
	case ModeSingle, "":
		return []string{targets[0].VirtualKey}, nil
	case ModeFallback:
		keys := make([]string, 0, len(targets))
		for _, t := range targets {
			keys = append(keys, t.VirtualKey)
		}
		return keys, nil
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
		return keys, nil
	case ModeContentBased:
		// Evaluate content rules in order; first match wins, fallback is targets[0].
		for _, cond := range g.streamingContent {
			if streamingContentConditionMatches(cond, req) {
				// Matched target first, then remaining targets as fallback.
				keys := []string{cond.TargetKey}
				for _, t := range targets {
					keys = appendUniqueKey(keys, t.VirtualKey)
				}
				return keys, nil
			}
		}
		// No rule matched — use declared target order (targets[0] is the fallback).
		keys := make([]string, 0, len(targets))
		for _, t := range targets {
			keys = append(keys, t.VirtualKey)
		}
		return keys, nil
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
					return keys, nil
				}
			}
			// Floating-point safety net — use last variant.
			last := g.config.Strategy.ABVariants[len(g.config.Strategy.ABVariants)-1]
			keys := []string{last.TargetKey}
			for _, t := range targets {
				keys = appendUniqueKey(keys, t.VirtualKey)
			}
			return keys, nil
		}
		// No variants configured — fall through to raw order.
		keys := make([]string, 0, len(targets))
		for _, t := range targets {
			keys = append(keys, t.VirtualKey)
		}
		return keys, nil
	case ModeLoadBalance:
		startIdx := weightedStartIndex(targets)
		keys := make([]string, 0, len(targets))
		for i := 0; i < len(targets); i++ {
			keys = append(keys, targets[(startIdx+i)%len(targets)].VirtualKey)
		}
		return keys, nil
	case ModeLatency:
		return g.streamingLatencyOrderLocked(targets, req), nil
	case ModeCostOptimized:
		return g.streamingCostOrderLocked(targets, req)
	default:
		keys := make([]string, 0, len(targets))
		for _, t := range targets {
			keys = append(keys, t.VirtualKey)
		}
		return keys, nil
	}
}

type streamingLatencyCandidate struct {
	key        string
	p50        time.Duration
	hasSamples bool
}

func (g *Gateway) streamingLatencyOrderLocked(targets []Target, req providers.Request) []string {
	var unseen []streamingLatencyCandidate
	var sampled []streamingLatencyCandidate
	for _, t := range targets {
		if !g.isStreamingTargetCandidateLocked(t, req.Model) {
			continue
		}
		p50, hasSamples := g.latencyTracker.Stats(t.VirtualKey)
		candidate := streamingLatencyCandidate{
			key:        t.VirtualKey,
			p50:        p50,
			hasSamples: hasSamples,
		}
		if candidate.hasSamples {
			sampled = append(sampled, candidate)
		} else {
			unseen = append(unseen, candidate)
		}
	}

	if len(unseen) == 0 && len(sampled) == 0 {
		return targetKeys(targets)
	}

	if len(unseen) > 1 {
		rand.Shuffle(len(unseen), func(i, j int) {
			unseen[i], unseen[j] = unseen[j], unseen[i]
		}) //nolint:gosec
	}
	sort.SliceStable(sampled, func(i, j int) bool {
		return sampled[i].p50 < sampled[j].p50
	})

	keys := make([]string, 0, len(targets))
	for _, candidate := range unseen {
		keys = appendUniqueKey(keys, candidate.key)
	}
	for _, candidate := range sampled {
		keys = appendUniqueKey(keys, candidate.key)
	}
	return appendRemainingTargetKeys(keys, targets)
}

type streamingCostCandidate struct {
	key        string
	costUSD    float64
	hasPrice   bool
	modelFound bool
}

func (g *Gateway) streamingCostOrderLocked(targets []Target, req providers.Request) ([]string, error) {
	estimatedPromptTokens := estimatePromptTokens(req)
	candidates := make([]streamingCostCandidate, 0, len(targets))
	for _, t := range targets {
		if !g.isStreamingTargetCandidateLocked(t, req.Model) {
			continue
		}
		result := models.Calculate(g.catalog, t.VirtualKey+"/"+req.Model, models.Usage{
			PromptTokens: estimatedPromptTokens,
		})
		candidates = append(candidates, streamingCostCandidate{
			key:        t.VirtualKey,
			costUSD:    result.InputUSD,
			hasPrice:   result.Priced,
			modelFound: result.ModelFound,
		})
	}
	if len(candidates) == 0 {
		return targetKeys(targets), nil
	}

	ranked := make([]streamingCostCandidate, 0, len(candidates))
	switch g.config.Strategy.UnpricedStrategy {
	case unpricedStrategyAllow:
		for _, candidate := range candidates {
			if candidate.modelFound {
				ranked = append(ranked, candidate)
			}
		}
	case unpricedStrategySkip:
		for _, candidate := range candidates {
			if candidate.modelFound && candidate.hasPrice {
				ranked = append(ranked, candidate)
			}
		}
	default:
		for _, candidate := range candidates {
			if candidate.modelFound && candidate.hasPrice {
				ranked = append(ranked, candidate)
			}
		}
	}

	if len(ranked) == 0 {
		if g.config.Strategy.UnpricedStrategy == unpricedStrategySkip {
			return nil, fmt.Errorf("no priced provider supports model %s", req.Model)
		}
		return targetKeys(targets), nil
	}

	sort.SliceStable(ranked, func(i, j int) bool {
		return ranked[i].costUSD < ranked[j].costUSD
	})

	keys := make([]string, 0, len(targets))
	for _, candidate := range ranked {
		keys = appendUniqueKey(keys, candidate.key)
	}
	for _, candidate := range candidates {
		keys = appendUniqueKey(keys, candidate.key)
	}
	return appendRemainingTargetKeys(keys, targets), nil
}

func (g *Gateway) isStreamingTargetCandidateLocked(t Target, model string) bool {
	p, ok := g.providers[t.VirtualKey]
	if !ok || !p.SupportsModel(model) {
		return false
	}
	_, ok = p.(providers.StreamProvider)
	return ok
}

func estimatePromptTokens(req providers.Request) int {
	promptChars := 0
	for _, msg := range req.Messages {
		promptChars += len(msg.Content)
	}
	return promptChars/4 + 1
}

func targetKeys(targets []Target) []string {
	keys := make([]string, 0, len(targets))
	for _, t := range targets {
		keys = append(keys, t.VirtualKey)
	}
	return keys
}

func appendRemainingTargetKeys(keys []string, targets []Target) []string {
	for _, t := range targets {
		keys = appendUniqueKey(keys, t.VirtualKey)
	}
	return keys
}

// modelsForRoutingLocked returns the model IDs used to build the exact-match
// routing index for a provider: the union of its hardcoded SupportedModels()
// and the catalog's models for that provider (issue #146). Deriving from the
// catalog lets exact-match providers route valid models their hand-maintained
// slices omit, without per-provider edits. Falls back to the hardcoded slice
// when the catalog has no entries for the provider (e.g. self-hosted Ollama).
func (g *Gateway) modelsForRoutingLocked(name string, p providers.Provider) []string {
	hardcoded := p.SupportedModels()
	catModels := g.catalog.ModelsForProvider(name)
	if len(catModels) == 0 {
		return hardcoded
	}
	seen := make(map[string]struct{}, len(hardcoded)+len(catModels))
	out := make([]string, 0, len(hardcoded)+len(catModels))
	for _, m := range hardcoded {
		if _, ok := seen[m]; !ok {
			seen[m] = struct{}{}
			out = append(out, m)
		}
	}
	for _, m := range catModels {
		if _, ok := seen[m]; !ok {
			seen[m] = struct{}{}
			out = append(out, m)
		}
	}
	return out
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
		models := g.modelsForRoutingLocked(name, p)
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

func compileStreamingContentConditions(mode StrategyMode, conditions []ContentCondition) ([]streamingContentCondition, error) {
	if mode != ModeContentBased {
		return nil, nil
	}
	compiled := make([]streamingContentCondition, len(conditions))
	for i, cond := range conditions {
		compiled[i].ContentCondition = cond
		if cond.Type != "prompt_regex" {
			continue
		}
		re, err := regexp.Compile(cond.Value)
		if err != nil {
			return nil, fmt.Errorf("streaming content-based routing: invalid regex %q in rule %d: %w", cond.Value, i, err)
		}
		compiled[i].re = re
	}
	return compiled, nil
}

// streamingContentConditionMatches evaluates a single ContentCondition against
// a request, mirroring the logic in internal/strategies/contentbased.go.
func streamingContentConditionMatches(cond streamingContentCondition, req providers.Request) bool {
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
		if cond.re == nil {
			return false
		}
		for _, msg := range req.Messages {
			if msg.Role == roleUser && cond.re.MatchString(msg.Content) {
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
		// Precedence (issue #146): live discovery > catalog > hardcoded fallback.
		if discovered, ok := g.discoveredModels[name]; ok && len(discovered) > 0 {
			models = append(models, discovered...)
		} else if catModels := g.catalog.ModelsForProvider(name); len(catModels) > 0 {
			models = append(models, core.ModelsFromList(name, catModels)...)
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
//
// Cancels the gateway shutdown context (which signals hook workers and the
// catalog refresh worker to exit) and waits up to 5s for workers to finish so
// in-flight hook dispatches are not abruptly killed. Returns nil even if the worker drain
// times out — Close must never block indefinitely (a panicking hook could
// otherwise wedge shutdown).
//
// Safe to call multiple times; subsequent calls are no-ops.
func (g *Gateway) Close() error {
	g.closeOnce.Do(func() {
		g.shutdownCancel()
		g.mu.Lock()
		plugins := g.plugins
		g.plugins = plugin.NewManager()
		g.mu.Unlock()
		if err := closePluginManager(plugins); err != nil {
			slog.Warn("plugin close failed during gateway shutdown", "error", err)
		}
		done := make(chan struct{})
		go func() {
			g.hookWorkersDone.Wait()
			g.catalogRefreshDone.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
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
