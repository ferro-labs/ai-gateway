package aigateway

import (
	"context"
	"errors"
	"fmt"
	"runtime/trace"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/authctx"
	"github.com/ferro-labs/ai-gateway/internal/strategies"
	"github.com/ferro-labs/ai-gateway/mcp"
	"github.com/ferro-labs/ai-gateway/observability"
	"github.com/ferro-labs/ai-gateway/pkg/logger"
	"github.com/ferro-labs/ai-gateway/pkg/metrics"
	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// runBeforePlugins runs before-request plugins and returns an early response when
// a plugin (e.g. response-cache) set SkipProvider, meaning the provider must not be
// called. Every other before-request plugin has still run by then — SkipProvider
// suppresses the upstream call, not the chain — and RunAfter is called before the
// early response is returned so the request is recorded like any other. It also
// propagates any request mutations the plugins made.
//
// The early response is handed to the after_request stage with the measurements
// that describe it: how long the gateway took, and a cost of zero. Handing it
// none left the stage reading the zero value, so the request that skipped the
// provider was recorded as having taken no time and having an UNKNOWN cost — the
// one shape that makes a reader assume the gateway simply could not price it.
//
// The manager runs the on_error stage itself when RunBefore aborts, so a request
// denied by a guardrail is on record before this returns.
func (g *Gateway) runBeforePlugins(ctx context.Context, plugins *plugin.Manager, pctx *plugin.Context, req *providers.Request, start time.Time) (*providers.Response, error) {
	if err := plugins.RunBefore(ctx, pctx); err != nil {
		return nil, err
	}
	if pctx.Request != nil {
		*req = *pctx.Request
	}
	if pctx.SkipProvider && pctx.Response != nil {
		pctx.Measurements = cacheServedMeasurements(start)
		if err := plugins.RunAfter(ctx, pctx); err != nil {
			pctx.Error = err
			plugins.RunOnError(ctx, pctx)
			return nil, err
		}
		return pctx.Response, nil
	}
	return nil, nil
}

// newPluginContext builds the plugin context for one request, or nil when this
// gateway has no plugins configured — which is what every caller tests to decide
// whether a plugin stage runs at all.
//
// It exists so the context is available BEFORE the before_request stage: a
// request refused by admission never reaches that stage and still has to reach
// on_error, and it can only do that through a context somebody built for it.
//
// span nests each plugin's child span under the request span, and the opaque key
// identifier is propagated so per-key plugins (rate-limit, budget) can scope
// limits to the authenticated caller. The raw bearer secret is never exposed
// here — only the stable APIKey.ID.
func (g *Gateway) newPluginContext(ctx context.Context, plugins *plugin.Manager, span observability.Span, req *providers.Request) *plugin.Context {
	if !plugins.HasPlugins() {
		return nil
	}
	pctx := plugin.NewContext(req)
	pctx.Span = span
	if keyID, ok := authctx.KeyID(ctx); ok {
		pctx.Metadata["api_key"] = keyID
	}
	return pctx
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
//
// It WRAPS context.DeadlineExceeded because it is one. net/http reports
// context.Cause on a cancelled request, so this sentinel — not the stdlib
// one — is what a provider's error actually carries out; a classifier asking
// the ordinary question "did this time out" got "no" and answered 500. The
// wrapping keeps the specific identity available to anything that needs it
// (errors.Is against this value still matches, and nothing else wraps it, so
// a caller-supplied deadline is still distinguishable) while making the
// general question answerable by any package, including ones that cannot
// import this one.
var ErrRequestTimeout = fmt.Errorf("gateway request timeout: %w", context.DeadlineExceeded)

// noopCancel is the CancelFunc returned when no per-request deadline applies. It
// is a package-level func, not a closure, so the default path allocates nothing.
// recordBeforePluginAbort records a request the before_request stage refused.
//
// A rejected request took time too, and the duration histogram is meant to
// answer "how fast is the gateway", not "how fast are the requests that
// worked". The root span has to carry the failure as well: a request a plugin
// denied never reaches the provider call that would otherwise mark it, so
// without this the trace shows a request that simply ended.
func (g *Gateway) recordBeforePluginAbort(span observability.Span, model string, start time.Time, err error) {
	h := metrics.ForRequest("", g.metricModel(model))
	h.Duration.Observe(time.Since(start).Seconds())
	recordPluginAbort(h, err)
	span.SetError(err)
}

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
//
//nolint:maintidx // The request lifecycle stays together so plugin, routing, MCP, and accounting stages cannot drift.
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
	releaseMCP := acquireMCPRegistry(mcpRegistrySnapshot)
	plugins := g.plugins
	releasePlugins := acquirePluginManager(plugins)
	g.mu.RUnlock()
	defer releasePlugins()
	defer releaseMCP()

	// Bound the whole non-streaming request — plugin stages, provider call, and
	// every retry/fallback attempt — when a request timeout is configured. The
	// deadline rides ctx, so the retry loop and provider clients already honor it.
	// RouteStream is deliberately exempt: a stream legitimately outlives any fixed
	// deadline. Its MCP path is not — it delegates here, and an agentic loop that
	// returns one complete chunk is a non-streaming request in all but name.
	ctx, cancelDeadline := withRequestDeadline(ctx, requestTimeout)
	defer cancelDeadline()

	ctx = withUnsupportedParamMode(ctx, compatMode)
	// Every entry point seeds its own trace ID, because this one is exported and
	// an embedder calling it directly has no HTTP middleware above it. Without
	// this the span, the request-log row and the lifecycle event below all
	// carried an empty trace ID. A request that came through Middleware already
	// carries a canonical one and this is a no-op.
	ctx = logger.EnsureTraceID(ctx)
	ctx, span := obs.StartRequestSpan(ctx, observability.RequestAttrs{
		Operation:       "chat",
		RequestModel:    req.Model,
		IsStream:        req.Stream,
		TraceID:         logger.TraceIDFromContext(ctx),
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

	// Whether the *caller* sent tools, captured before before_request plugins
	// can mutate req.Tools. MCP participation is a statement about the caller's
	// request, not about what a transform plugin later added — and RouteStream
	// decides whether to divert using this same pre-plugin state. Reading the
	// post-plugin slice instead let a plugin that adds tools flip MCP off after
	// the stream had already been diverted, returning one buffered chunk to a
	// caller who was entitled to a real stream.
	callerSentTools := len(req.Tools) > 0

	s, err := g.getStrategy()
	if err != nil {
		return nil, err
	}

	// Run before-request plugins (guardrails, transforms, rate-limit).
	pctx := g.newPluginContext(ctx, plugins, span, &req)
	if pctx != nil {
		defer plugin.PutContext(pctx)
	}

	// Admission before the plugin stage, so a model no target serves cannot
	// spend a rate-limit token or a budget on the way to its 404. routeError
	// still runs the on_error stage, so the request is recorded. It stands down
	// when a transform plugin may still rewrite the model. See admitModel.
	if err := g.admitModel(ctx, plugins, req.Model, nil); err != nil {
		g.routeError(ctx, span, obs, pctx, plugins, "", req.Model, "", false, err, time.Since(start), originalStream, hooksEnabled, obsEventsActive)
		return nil, err
	}

	if pctx != nil {
		var early *providers.Response
		trace.WithRegion(ctx, "gateway.route.plugins.before", func() {
			early, err = g.runBeforePlugins(ctx, plugins, pctx, &req, start)
		})
		if err != nil {
			g.recordBeforePluginAbort(span, req.Model, start, err)
			return nil, err
		}
		if early != nil {
			early.Model = req.Model
			if early.Object == "" {
				early.Object = "chat.completion"
			}
			if early.Created == 0 {
				early.Created = time.Now().Unix()
			}
			earlyLatency := time.Since(start)
			g.recordCacheHit(ctx, span, obs, early, earlyLatency, originalStream, hooksEnabled, obsEventsActive)
			early.OverheadMs = float64(earlyLatency.Microseconds()) / 1000.0
			return early, nil
		}
	}

	// Inject MCP tool definitions into the request when servers are ready.
	var mcpTools []mcp.Tool
	if mcpRegistrySnapshot != nil {
		mcpTools = mcpRegistrySnapshot.AllTools()
	}

	// MCP participates only when the caller supplied no tools of its own.
	//
	// Advertising both sets manufactures a turn neither party can resolve. The
	// model may answer with one MCP call and one caller call; the gateway cannot
	// execute the caller's (it has no implementation), and the caller cannot
	// execute the MCP one (it never declared it and does not know the server
	// exists). Answering only the gateway's half leaves an unmatched
	// tool_call_id, which every OpenAI-compatible provider rejects — so the
	// executor declines such turns outright (mcp.Executor.ownsAll).
	// Declining is the correct behaviour once the turn exists; not creating it
	// is the correct fix. A caller that sent its own tools keeps the plain
	// pass-through it asked for, streaming included.
	mcpActive := len(mcpTools) > 0 && !callerSentTools

	provReq := providerRequestFor(req, mcpTools, mcpActive)

	// Run the request through the pipeline: the strategy's target order, then
	// per-target retry, breaker and concurrency limit around each provider call.
	var resp *providers.Response
	var target routedTarget
	var providerDuration time.Duration
	providerStart := time.Now()
	trace.WithRegion(ctx, "gateway.route.provider.execute", func() {
		resp, target, err = g.routeChat(ctx, s, provReq)
	})
	providerDuration += time.Since(providerStart)
	latency := time.Since(start)
	// Stamped now for the plain case and again below when the MCP loop
	// re-routes, so the span carries the attempt count of the target that
	// actually answered.
	if target.key != "" {
		span.SetAttribute(observability.AttrFerroRoutingAttempt, target.attempts)
	}

	if err != nil {
		// target names the last target actually attempted. Reporting "" here
		// left every non-streaming provider failure in one unlabelled bucket,
		// which is the one thing per-provider alerting needs.
		g.routeError(ctx, span, obs, pctx, plugins, target.key, req.Model, target.abVariantLabel, target.hasABVariant, err, latency, originalStream, hooksEnabled, obsEventsActive)
		return nil, err
	}
	// Ensure OpenAI-compatible envelope fields are always set.
	if resp.Object == "" {
		resp.Object = "chat.completion"
	}
	if resp.Created == 0 {
		resp.Created = time.Now().Unix()
	}

	// Latency for the least-latency strategy is recorded inside the pipeline,
	// against the target key and covering the provider call alone — the same
	// measurement the streaming and non-chat surfaces already record, and the
	// same key LeastLatency reads its samples back by.

	// Agentic MCP tool-call loop. Runs only when MCP is active and the LLM
	// returned tool_calls. Each iteration executes the tools and re-contacts
	// the LLM until no more tool_calls are present or the depth limit is hit.
	if mcpExecutorSnapshot != nil && mcpActive {
		var loopDuration time.Duration
		var loopTarget routedTarget
		var loopUsage core.Usage
		resp, loopUsage, loopDuration, loopTarget, err = g.runMCPLoop(ctx, mcpExecutorSnapshot, plugins, pctx, s, &provReq, resp, target)
		providerDuration += loopDuration
		if err != nil {
			// The turns that completed before the failure spent real tokens, so
			// they are billed. Reporting only the error charged nothing for
			// them, which made a prompt that reliably fails late free.
			if pctx != nil {
				pctx.Response = &providers.Response{Model: req.Model, Provider: loopTarget.key, Usage: loopUsage}
			}
			if loopTarget.key != "" {
				span.SetAttribute(observability.AttrFerroRoutingAttempt, loopTarget.attempts)
				recordAttribution(ctx, loopTarget, loopTarget.attempts)
			}
			g.routeError(ctx, span, obs, pctx, plugins, loopTarget.key, req.Model, loopTarget.abVariantLabel, loopTarget.hasABVariant, err, time.Since(start), originalStream, hooksEnabled, obsEventsActive)
			return nil, err
		}
		// The loop re-contacts the provider, so the target that answered LAST is
		// the one this request finished on — the same rule the failure path
		// already applies to loopTarget.
		if loopTarget.key != "" {
			target = loopTarget
			span.SetAttribute(observability.AttrFerroRoutingAttempt, target.attempts)
			recordAttribution(ctx, target, target.attempts)
		}
	}
	// Provider-returned model identifiers are upstream payload detail. The
	// routed post-alias model is the public identity on every gateway surface.
	// Apply this after the MCP loop because its final turn can replace resp.
	resp.Model = req.Model
	// originalStream is included in the completed event so hook consumers
	// can distinguish streaming vs non-streaming requests (Phase 1.5 note:
	// when final-response streaming lands, remove the force-to-false above).

	// Cost is computed here rather than only in recordSuccess below, because
	// the after-request plugins need it: the request logger persists it, and it
	// cannot be recovered later. recordSuccess is handed the same result rather
	// than repeating the calculation.
	cost := g.calculateCost(resp, target.priceProvider, target.upstreamModel)

	// Measured before the stage runs, so the duration a logging plugin records
	// does not include that plugin. Streaming requests take the other path, in
	// RouteStream, where a time to first token also exists.
	resp, err = g.runAfterPlugins(ctx, plugins, pctx, resp, target.key, plugin.Measurements{
		DurationMs: float64(time.Since(start).Microseconds()) / 1000.0,
		CostUSD:    cost.TotalUSD,
		HasCost:    cost.Priced,
	})
	if err != nil {
		// runAfterPlugins already ran on_error, so finish through failed
		// accounting without running it twice. The request is timed and
		// counted as the plugin abort it is — the provider call succeeded, so
		// it is never a provider failure.
		latency = time.Since(start)
		g.recordFailureMetrics(ctx, target.key, req.Model, err, latency)
		g.recordTerminalFailure(ctx, span, obs, target.key, req.Model, target.abVariantLabel, target.hasABVariant, err, latency, originalStream, hooksEnabled, obsEventsActive)
		return nil, err
	}

	// Emit metrics + cost, stamp the span, and dispatch the completed event.
	// Refresh latency so final accounting covers the whole request, including
	// any MCP tool-call loop iterations — keeping it consistent with the
	// accumulated providerDuration so OverheadMs stays non-negative.
	latency = time.Since(start)
	g.recordSuccess(ctx, span, obs, resp, req.Model, cost, latency, target.abVariantLabel, target.hasABVariant, originalStream, hooksEnabled, obsEventsActive)

	resp.OverheadMs = float64((latency - providerDuration).Microseconds()) / 1000.0

	return resp, nil
}

// providerRequestFor returns the request the provider call should carry, which
// is NOT always the request the plugin stages observe.
//
// req is taken by value, so the tools land on a copy. That is the whole point:
// MCP is the gateway's own way of serving the call rather than something the
// caller asked for, and plugin.Context holds a POINTER to the caller's request.
// Injecting in place made every plugin after the before_request stage read
// gateway-injected tools as though the client had sent them.
//
// The response cache is where that was load-bearing. It keys on tools, quite
// correctly, and computes that key twice — once to look up at before_request and
// once to store at after_request. Mutating the request in between meant the two
// keys could never match, so configuring ANY mcp_servers entry silently disabled
// response caching for the whole deployment. No tool call was required; the
// plugin reported itself configured and simply never hit.
//
// The copy is shallow, which is sound because injectMCPTools only assigns:
// active requires that the caller sent no tools, so the append inside allocates
// rather than writing through a backing array the original still shares.
func providerRequestFor(req providers.Request, tools []mcp.Tool, active bool) providers.Request {
	if active {
		injectMCPTools(&req, tools)
	}
	return req
}

// injectMCPTools advertises the gateway's MCP tools on req and forces the call
// non-streaming.
//
// The two go together. During the agentic loop an intermediate call must be
// non-streaming so the whole response can be inspected for tool_calls; the
// client's original stream preference is captured before this and restored on
// the final response (Phase 1: an MCP request always returns non-streaming).
func injectMCPTools(req *providers.Request, tools []mcp.Tool) {
	for _, t := range tools {
		req.Tools = append(req.Tools, core.Tool{
			Type: "function",
			Function: core.Function{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		})
	}
	req.Stream = false
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
func (g *Gateway) runMCPLoop(ctx context.Context, mcpExecutorSnapshot *mcp.Executor, plugins *plugin.Manager, pctx *plugin.Context, s strategies.Strategy, req *providers.Request, resp *providers.Response, initialTarget routedTarget) (*providers.Response, core.Usage, time.Duration, routedTarget, error) {
	var providerDuration time.Duration
	var err error
	depth := 0
	loopTarget := initialTarget
	// Attempts accumulate across turns: an HTTP request that made three
	// provider calls reports three, on the identity of the target that
	// answered last.
	attempts := initialTarget.attempts
	// What this request has spent so far, fed to the per-turn budget check.
	var (
		spentUSD    float64
		spentPriced bool
	)

	// Token usage accumulates across turns, exactly as the provider duration
	// above already does. Each turn is a real provider call spending real
	// tokens, but resp is overwritten per turn, so only the FINAL turn's usage
	// survived the loop — and calculateCost, recordSuccess, the request-log row
	// and the budget guardrail all read resp.Usage. An eight-turn agentic
	// request was therefore priced, metered and billed as one turn. Seeded with
	// the turn the caller already made before entering the loop.
	//
	// summed here and priced once by the caller, at the final turn's
	// provider rate. Under loadbalance or least-latency an intermediate turn can
	// land on a differently-priced provider, so the total is approximate to that
	// extent — a known ceiling, and a far smaller error than dropping the turn
	// entirely. Per-turn pricing is not reachable from this file: the budget
	// guardrail and the request-log row price from resp.Usage themselves, so a
	// per-turn CostResult carried out of here would disagree with what those two
	// bill. Upgrade path: carry a per-turn cost breakdown on plugin.Measurements
	// and have budget read it, then price inside the loop.
	total := resp.Usage

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

			// Every turn is a provider call, so every turn faces the plugins that
			// bound one. Without this the messages appended just above — tool
			// results from an EXTERNAL server — reached the provider, and came
			// back to the client, with no configured guardrail having read them.
			//
			// The running cost goes with it so a budget can act on what this
			// request has already spent: the store it reads is only written after
			// the whole request, so a per-turn check without this term reads the
			// same figure every turn and stops nothing.
			if pctx != nil && plugins != nil {
				// Point the context at the request the loop is BUILDING, not the
				// one the caller sent. They are different objects: MCP tools and
				// the tool results below land on a copy, so that the response
				// cache and the after stage still see what the client actually
				// asked for. A guardrail has the opposite need — it must read
				// what is about to go on the wire — so the pointer is swapped for
				// the duration of the check and restored immediately.
				callerRequest := pctx.Request
				pctx.Request = req
				pctx.Measurements.CostUSD, pctx.Measurements.HasCost = spentUSD, spentPriced
				turnErr := plugins.RunBeforeLoopTurn(ctx, pctx)
				pctx.Request = callerRequest
				if turnErr != nil {
					err = turnErr
					return
				}
				if pctx.Reject {
					err = &plugin.RejectionError{Reason: pctx.Reason}
					return
				}
			}

			callStart := time.Now()
			var target routedTarget
			resp, target, err = g.routeChat(ctx, s, *req)
			providerDuration += time.Since(callStart)
			if target.key != "" {
				attempts += target.attempts
				loopTarget = target
				loopTarget.attempts = attempts
			}
			if err != nil {
				return
			}
			total.PromptTokens += resp.Usage.PromptTokens
			total.CompletionTokens += resp.Usage.CompletionTokens
			total.TotalTokens += resp.Usage.TotalTokens
			total.ReasoningTokens += resp.Usage.ReasoningTokens
			total.CacheReadTokens += resp.Usage.CacheReadTokens
			total.CacheWriteTokens += resp.Usage.CacheWriteTokens

			// Priced per turn rather than once at the end, because the budget
			// check above needs it before the NEXT call is made. Under
			// loadbalance or least-latency consecutive turns can land on
			// differently-priced providers, and pricing each against the provider
			// that served it is strictly better than pricing the total at the
			// last one's rate.
			if turnCost := g.calculateCost(resp, target.priceProvider, target.upstreamModel); turnCost.Priced {
				spentUSD += turnCost.TotalUSD
				spentPriced = true
			}
		}
		// Stamped on the response the caller returns, so every consumer that
		// already reads resp.Usage gets the whole request's usage without a new
		// field or a signature change.
		resp.Usage = total
	})
	// total is returned separately as well, because the error paths above leave
	// resp holding either the last good turn or nothing at all. A loop that
	// failed on turn three still spent turns one and two, and returning only an
	// error billed the caller nothing for them — free, and steerable by any
	// prompt that reliably fails late.
	return resp, total, providerDuration, loopTarget, err
}
