package aigateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/trace"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/authctx"
	"github.com/ferro-labs/ai-gateway/internal/circuitbreaker"
	"github.com/ferro-labs/ai-gateway/internal/events"
	"github.com/ferro-labs/ai-gateway/internal/logging"
	"github.com/ferro-labs/ai-gateway/internal/mcp"
	"github.com/ferro-labs/ai-gateway/internal/metrics"
	"github.com/ferro-labs/ai-gateway/internal/strategies"
	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/observability"
	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// This file holds the non-streaming request path for the Gateway: Route and its
// before-plugin, lifecycle-event dispatch, and success-recording helpers. Split
// out of gateway.go; still part of package aigateway (no behavior change).

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

// recordPluginAbort counts a request a plugin cut short, against the counter that
// tells the truth about why: a deliberate rejection is a rejection, a broken plugin
// is an error. Folding both into "rejected" would make a plugin outage look like a
// wave of blocked prompts — the one shape that would send an operator hunting
// through guardrail rules while the actual fault is a dead Redis.
func recordPluginAbort(h *metrics.RequestMetricHandles, err error) {
	var failure *plugin.FailureError
	if errors.As(err, &failure) {
		h.Error.Inc()
		return
	}
	h.Rejected.Inc()
}

// ErrRequestTimeout is the cause attached to a context cancelled by the gateway's
// own per-request deadline (Config.RequestTimeout). Retrieve it with
// context.Cause(ctx).
//
// It exists so a deadline the GATEWAY imposed can be told apart from a
// caller-side cancellation or a caller-supplied deadline. That distinction is
// load-bearing: when the gateway's deadline fires, the provider was too slow to
// answer — a provider failure that must trip its circuit breaker. When the caller
// walks away, the provider is not at fault and its breaker must be left alone.
var ErrRequestTimeout = errors.New("gateway request timeout")

// noopCancel is the CancelFunc returned when no per-request deadline applies. It
// is a package-level func, not a closure, so the default path allocates nothing.
func noopCancel() {}

// requestDeadline returns the configured per-request timeout, or 0 when none is
// set. Config.RequestTimeout is validated at load, so an unparseable or
// non-positive value can only reach here from a programmatically-built Config;
// it is treated as "no deadline" rather than failing an otherwise valid request.
// The empty case short-circuits before parsing, so the default hot path neither
// parses nor allocates.
func requestDeadline(requestTimeout string) time.Duration {
	if requestTimeout == "" {
		return 0
	}
	d, err := time.ParseDuration(requestTimeout)
	if err != nil || d <= 0 {
		return 0
	}
	return d
}

// withRequestDeadline bounds ctx by the configured per-request timeout, tagging
// the cancellation with ErrRequestTimeout so downstream code can attribute the
// deadline to the gateway rather than to the caller. It returns ctx untouched,
// with a no-op cancel, when no timeout is configured.
func withRequestDeadline(ctx context.Context, requestTimeout string) (context.Context, context.CancelFunc) {
	d := requestDeadline(requestTimeout)
	if d <= 0 {
		return ctx, noopCancel
	}
	return context.WithTimeoutCause(ctx, d, ErrRequestTimeout)
}

// withUnsupportedParamMode carries the configured compatibility mode to the
// shared request builder via ctx. It only decorates ctx when a non-default
// mode is configured, so the warn (default) hot path keeps its
// zero-allocation baseline.
func withUnsupportedParamMode(ctx context.Context, compatMode string) context.Context {
	if mode, _ := core.ParseUnsupportedParamMode(compatMode); mode != core.UnsupportedParamWarn {
		return core.WithUnsupportedParamMode(ctx, mode)
	}
	return ctx
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
	compatMode := g.config.Compatibility.OnUnsupportedParam
	requestTimeout := g.config.RequestTimeout
	obs := g.obs
	obsEventsActive := g.obsEventsActive
	mcpRegistrySnapshot := g.mcpRegistry
	mcpExecutorSnapshot := g.mcpExecutor
	plugins := g.plugins
	releasePlugins := acquirePluginManager(plugins)
	g.mu.RUnlock()
	defer releasePlugins()

	// Bound the whole non-streaming request — plugin stages, provider call, and
	// every retry/fallback attempt — when a request timeout is configured. The
	// deadline rides ctx, so the retry loop and provider clients already honor it.
	// RouteStream is deliberately exempt: a stream legitimately outlives any fixed
	// deadline. Its MCP path is not — it delegates here, and an agentic loop that
	// returns one complete chunk is a non-streaming request in all but name.
	ctx, cancelDeadline := withRequestDeadline(ctx, requestTimeout)
	defer cancelDeadline()

	ctx = withUnsupportedParamMode(ctx, compatMode)
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

	// Captured before the agentic MCP loop forces req.Stream = false, and
	// before any early plugin short-circuit, so hook/observability consumers
	// always see the client's requested stream preference.
	originalStream := req.Stream

	s, err := g.getStrategy()
	if err != nil {
		return nil, err
	}

	// Run before-request plugins (guardrails, transforms, rate-limit).
	var pctx *plugin.Context
	if plugins.HasPlugins() {
		pctx = plugin.NewContext(&req)
		pctx.Span = span // per-plugin child spans nest under the request span.
		defer plugin.PutContext(pctx)
		// Propagate the opaque key identifier so per-key plugins (rate-limit,
		// budget) can scope limits to the authenticated caller. The raw bearer
		// secret is never exposed here — only the stable APIKey.ID.
		if keyID, ok := authctx.KeyID(ctx); ok {
			pctx.Metadata["api_key"] = keyID
		}
		var early *providers.Response
		trace.WithRegion(ctx, "gateway.route.plugins.before", func() {
			early, err = g.runBeforePlugins(ctx, plugins, pctx, &req)
		})
		if err != nil {
			recordPluginAbort(metrics.ForRequest("", g.metricModel(req.Model)), err)
			return nil, err
		}
		if early != nil {
			if early.Object == "" {
				early.Object = "chat.completion"
			}
			if early.Created == 0 {
				early.Created = time.Now().Unix()
			}
			earlyLatency := time.Since(start)
			g.recordSuccess(ctx, span, obs, early, earlyLatency, originalStream, hooksEnabled, obsEventsActive)
			early.OverheadMs = float64(earlyLatency.Microseconds()) / 1000.0
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
	// stream preference (captured above) is restored on the final response
	// (Phase 1: always returns non-streaming for MCP requests).
	if len(mcpTools) > 0 {
		req.Stream = false
	}

	// Execute the strategy (provider selection + actual call).
	var resp *providers.Response
	var providerDuration time.Duration
	providerStart := time.Now()
	trace.WithRegion(ctx, "gateway.route.provider.execute", func() {
		resp, err = s.Execute(ctx, req)
	})
	providerDuration += time.Since(providerStart)
	latency := time.Since(start)

	if err != nil {
		g.routeError(ctx, span, obs, pctx, plugins, "", req.Model, err, latency, originalStream, hooksEnabled, obsEventsActive)
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
		var loopDuration time.Duration
		var loopProvider string
		resp, loopDuration, loopProvider, err = g.runMCPLoop(ctx, mcpExecutorSnapshot, s, &req, resp)
		providerDuration += loopDuration
		if err != nil {
			g.routeError(ctx, span, obs, pctx, plugins, loopProvider, req.Model, err, time.Since(start), originalStream, hooksEnabled, obsEventsActive)
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
			recordPluginAbort(metrics.ForRequest(resp.Provider, resp.Model), err)
			pctx.Error = err
			plugins.RunOnError(ctx, pctx)
			return nil, err
		}
		if pctx.Response != nil {
			resp = pctx.Response
		}
	}

	// Emit metrics + cost, stamp the span, and dispatch the completed event.
	// Refresh latency so final accounting covers the whole request, including
	// any MCP tool-call loop iterations — keeping it consistent with the
	// accumulated providerDuration so OverheadMs stays non-negative.
	latency = time.Since(start)
	g.recordSuccess(ctx, span, obs, resp, latency, originalStream, hooksEnabled, obsEventsActive)

	resp.OverheadMs = float64((latency - providerDuration).Microseconds()) / 1000.0

	return resp, nil
}

// metricModel bounds the Prometheus "model" label. Client-supplied model names
// reach the reject and error paths verbatim, so any name the gateway cannot
// route is collapsed to metrics.UnknownModelLabel rather than minting a new
// time series. The exact-match routing index is the authoritative set of known
// model IDs and is already rebuilt whenever providers or discovery change.
func (g *Gateway) metricModel(model string) string {
	if model == "" {
		return metrics.UnknownModelLabel
	}
	g.mu.RLock()
	_, known := g.modelIndex.exactProviders[model]
	g.mu.RUnlock()
	if known {
		return model
	}
	return metrics.UnknownModelLabel
}

// runMCPLoop drives the agentic MCP tool-call loop: while resp has pending
// tool_calls, it executes them via the MCP executor, appends the results to
// req, and re-contacts the provider (always non-streaming for intermediate
// calls) until no tool_calls remain or the depth limit is hit. req is
// mutated in place (same aliasing as runBeforePlugins) so the caller's
// plugin.Context, which was built from &req, still sees the accumulated
// tool messages. Returns the final response, the accumulated provider-call
// duration (for OverheadMs accounting), the provider name of the last
// attempted call (for error reporting), and any error.
func (g *Gateway) runMCPLoop(ctx context.Context, mcpExecutorSnapshot *mcp.Executor, s strategies.Strategy, req *providers.Request, resp *providers.Response) (*providers.Response, time.Duration, string, error) {
	var providerDuration time.Duration
	var err error
	depth := 0
	loopProvider := resp.Provider
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

			callStart := time.Now()
			resp, err = s.Execute(ctx, *req)
			providerDuration += time.Since(callStart)
			if err != nil {
				return
			}
			loopProvider = resp.Provider
		}
	})
	return resp, providerDuration, loopProvider, err
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

// routeError finalizes a failed Route call: runs plugin error hooks, records
// error metrics, stamps the span with the error, logs the failure, and
// dispatches the failed lifecycle event. Shared by the initial provider call
// and the MCP tool-call loop's follow-up provider calls so both error paths
// stay in sync.
func (g *Gateway) routeError(ctx context.Context, span observability.Span, obs observability.Provider, pctx *plugin.Context, plugins *plugin.Manager, provider, model string, err error, latency time.Duration, originalStream, hooksEnabled, obsEventsActive bool) {
	if pctx != nil {
		pctx.Error = err
		plugins.RunOnError(ctx, pctx)
	}

	errType := "provider_error"
	if errors.Is(err, circuitbreaker.ErrCircuitOpen) {
		errType = "circuit_open"
	}
	// Bucket the label, not the log/span: model here is still the raw client
	// value on the "no provider supports this model" path.
	metrics.ForRequest(provider, g.metricModel(model)).Error.Inc()
	metrics.ForProviderError(provider, errType).Inc()

	span.SetError(err)

	logging.FromContext(ctx).Error("request failed",
		"model", model,
		"latency_ms", latency.Milliseconds(),
		"error", err.Error(),
	)

	if hooksEnabled || obsEventsActive {
		he := failedEventData(
			logging.TraceIDFromContext(ctx),
			provider,
			model,
			err.Error(),
			latency,
			originalStream,
		)
		g.dispatchRequestEvent(ctx, obs, hooksEnabled, obsEventsActive, he)
	}
}

// recordSuccess emits Prometheus + cost metrics, stamps the root span with the
// resolved provider/model/usage/cost, logs at debug level, and dispatches the
// completed lifecycle event.
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
