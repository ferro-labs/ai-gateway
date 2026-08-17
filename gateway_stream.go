package aigateway

import (
	"context"
	"errors"
	"runtime/trace"
	"sync"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/events"
	"github.com/ferro-labs/ai-gateway/internal/streamwrap"
	"github.com/ferro-labs/ai-gateway/observability"
	"github.com/ferro-labs/ai-gateway/pkg/logger"
	"github.com/ferro-labs/ai-gateway/pkg/metrics"
	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
)

// streamAfterBudget bounds the detached after_request stage on the streaming
// path. Dropping the request's cancellation also drops its deadline, and a
// stage that only records what already happened must not be able to hold the
// metering goroutine open on an unresponsive store. It matches the bound the
// on_error stage applies for the same reason.
const streamAfterBudget = 10 * time.Second

// RouteStream runs before-request plugins then returns a metered streaming
// response channel. The stream START goes through the same request pipeline
// /v1/chat/completions uses, so target ordering, per-target retry, the circuit
// breaker, the concurrency limit and error classification are identical on both
// surfaces; when no configured target serves the model the request is refused,
// because targets is an allowlist. Once CompleteStream succeeds, nothing is
// retried or replayed. Target selection, each CompleteStream call, and the
// retry/backoff waits between them are bounded by Config.RequestTimeout, if
// configured; a stream that does start is never bounded by it once its channel
// is visible below — see startStreamWithStrategy and raceCompleteStream.
// Prometheus metrics and event hooks are emitted when the returned channel
// drains (matching the behaviour of Route for non-streaming).
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
	compatMode := g.config.Compatibility.OnUnsupportedParam
	requestTimeout := g.config.RequestTimeout
	obs := g.obs
	obsEventsActive := g.obsEventsActive
	mcpRegistrySnapshot := g.mcpRegistry
	plugins := g.plugins
	releasePlugins := acquirePluginManager(plugins)
	g.mu.RUnlock()

	ctx = withUnsupportedParamMode(ctx, compatMode)
	var releasePluginsOnce sync.Once
	releasePluginManager := func() {
		releasePluginsOnce.Do(releasePlugins)
	}
	// See Route: this entry point is exported too, and the trace ID read here is
	// the one every later read in this function (the meter meta, the span
	// finisher, the failure path) inherits from ctx.
	ctx = logger.EnsureTraceID(ctx)
	ctx, span := obs.StartRequestSpan(ctx, observability.RequestAttrs{
		Operation:       "chat",
		RequestModel:    req.Model,
		IsStream:        true,
		TraceID:         logger.TraceIDFromContext(ctx),
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

	// MCP redirect: when tool servers have advertised tools, the agentic loop
	// must run to completion before any response is sent. Route() handles this
	// entirely; we wrap its non-streaming result into a channel here.
	//
	// Gate on discovered tools, not on registration. HasServers() is true from
	// the moment a server is registered — before the handshake, and forever
	// after one that failed — so gating on it let a single unreachable or
	// typo'd MCP server silently collapse streaming into one buffered chunk for
	// every caller on the gateway.
	//
	// The caller-tools condition mirrors Route's mcpActive: when the caller
	// supplied its own tools MCP does not participate at all, so there is no
	// agentic loop to buffer for and the stream must be left alone. Both paths
	// must agree, or a request would be diverted here and then pass straight
	// through Route as an ordinary non-streaming call.
	if mcpRegistrySnapshot != nil && len(req.Tools) == 0 && len(mcpRegistrySnapshot.AllTools()) > 0 {
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

	// Admission before the plugin stage, so a model no target can stream cannot
	// spend a rate-limit token or a budget on the way to its 404. The on_error
	// stage still runs, so the request is recorded. It stands down when a
	// transform plugin may still rewrite the model. See admitModel.
	if err := g.admitModel(ctx, plugins, req.Model, streamCapable); err != nil {
		g.recordStreamStartFailure(ctx, span, obs, plugins, g.newPluginContext(ctx, plugins, span, &req), "", req.Model, err, start, hooksEnabled, obsEventsActive)
		releasePluginManager()
		return nil, err
	}

	// Run before-request plugins (word-filter, max-token, rate-limit, etc.).
	pctx, early, err := g.runBeforePluginsStream(ctx, span, obs, plugins, releasePluginManager, &req, start, hooksEnabled, obsEventsActive)
	if err != nil {
		return nil, err
	}
	if early != nil {
		return responseStream(early), nil
	}

	// Select and start the provider according to strategy mode. This is the
	// only safe retry window: CompleteStream has not returned a channel yet,
	// so no bytes can have reached the client. startCtx bounds that window
	// (target selection, each CompleteStream call, and the retry/backoff waits
	// between them) to RequestTimeout, so a hanging or endlessly-retrying
	// provider can no longer hold this goroutine open indefinitely. startCtx is
	// never the context CompleteStream actually runs on (see
	// raceCompleteStream below), so a stream that starts successfully keeps
	// running on the plain, undeadlined ctx and is not torn down once
	// RequestTimeout elapses — cancelStart only ever releases startCtx's own
	// timer, deferred here purely so a panic can't leak it.
	startCtx, cancelStart := withRequestDeadline(ctx, requestTimeout)
	defer cancelStart()
	providerName, rawCh, targetBreaker, err := g.startStreamWithStrategy(startCtx, ctx, req)
	span.SetAttribute(observability.AttrGenAISystem, providerName)
	// Stamp the resolved target key (virtual key = provider name in this routing layer).
	if providerName != "" {
		span.SetAttribute(observability.AttrFerroRoutingTargetKey, providerName)
	}
	if err == nil && g.log.Enabled(ctx, logger.LevelDebug) {
		g.log.Ctx(ctx).Debug("stream request started", "model", req.Model, "provider", providerName)
	}
	if err != nil {
		g.recordStreamStartFailure(ctx, span, obs, plugins, pctx, providerName, req.Model, err, start, hooksEnabled, obsEventsActive)
		releasePluginManager()
		return nil, err
	}

	// Wrap the raw channel with a metering goroutine that emits Prometheus
	// metrics and event hooks once the stream completes.
	g.mu.RLock()
	catalog := g.catalog
	// Resolved once, alongside the catalog snapshot, from the same live
	// provider set routing just selected providerName from — see
	// canonicalPriceKeyLocked. providerName itself keeps naming the routing
	// target on meta.Provider (attribution: metric labels, the completed/failed
	// event, resp.Provider); priceProvider exists solely for the cost lookup.
	priceProvider := g.canonicalPriceKeyLocked(providerName)
	g.mu.RUnlock()

	meta := streamwrap.MeterMeta{
		Provider:      providerName,
		PriceProvider: priceProvider,
		Model:         req.Model,
		// Model stays raw for cost lookup and event payloads; only the metric
		// label is bounded, mirroring the non-streaming path's use of the
		// provider-reported model.
		MetricModel:     g.metricModel(req.Model),
		Catalog:         catalog,
		TraceID:         logger.TraceIDFromContext(ctx),
		LatencyRecorder: g.latencyTracker.Record,
		// Usage is always requested upstream so metering, cost, and the budget
		// plugin see real numbers; a caller that asked not to receive it just
		// does not get the chunk forwarded.
		SuppressUsageForClient: req.ClientStreamOptions != nil && !req.ClientStreamOptions.IncludeUsage,
	}
	if hooksEnabled {
		meta.PublishFn = g.publishEvent
	}
	// The breaker's outcome is resolved when the STREAM ends, not when the start
	// call returns — the pipeline was told as much (responseOutlivesCall), so it
	// left the probe cbProvider admitted still held and nothing else will
	// resolve it. targetBreaker is the INSTANCE that admitted the probe, handed
	// back by the pipeline — a lookup by name here would race ReloadConfig,
	// which can retire that instance and install a fresh one under the same key
	// while the start is in flight.
	if targetBreaker != nil {
		meta.CircuitBreakerOutcome = func(err error) {
			recordCircuitBreakerOutcome(ctx, targetBreaker, providerName, err)
		}
	}
	if pctx != nil {
		meta.CompletionFn = func(ctx context.Context, resp *providers.Response, m streamwrap.Measurements) error {
			// The after stage runs from the metering goroutine, which outlives
			// the handler. By the time it runs the response has been delivered
			// in full, so the client's connection is no longer a reason to
			// abandon the bookkeeping it records — and a client that hangs up as
			// the last chunk lands cancels the request context in exactly that
			// window. The stage's durable success row is the only terminal row a
			// completed stream ever writes, so losing it leaves the request
			// visible in no operator-facing surface. Values and the trace context
			// are preserved, so the row still carries the request's trace ID.
			// Cancellation is replaced by streamAfterBudget rather than removed.
			//
			// The on_error stage does this for itself; this is the same
			// reasoning for the stage that records a success. The non-streaming
			// path is deliberately left alone: there RunAfter runs inside Route,
			// before the handler has answered, so it is still the caller's
			// request and its cancellation still applies.
			ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), streamAfterBudget)
			defer cancel()

			pctx.Response = resp
			pctx.Target = providerName
			// Time to first token exists only on this path, and only until the
			// channel has drained — an after_request plugin cannot derive it.
			pctx.Measurements = plugin.Measurements{
				DurationMs: m.DurationMs,
				TTFTMs:     m.TTFTMs,
				HasTTFT:    m.HasTTFT,
				CostUSD:    m.CostUSD,
				HasCost:    m.HasCost,
			}
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
		meta.ErrorFn = func(ctx context.Context, err error, m streamwrap.Measurements) {
			if pctx == nil {
				return
			}
			pctx.Error = err
			// A stream that dies after its first chunk is the failure with the
			// least evidence anywhere else, and naming the provider it died on is
			// most of what the row is for.
			pctx.Target = providerName
			// No cost: the request was never billed, and no usage reached the
			// catalog to price. The timings up to the failure are real.
			pctx.Measurements = plugin.Measurements{
				DurationMs: m.DurationMs,
				TTFTMs:     m.TTFTMs,
				HasTTFT:    m.HasTTFT,
			}
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
	traceID := logger.TraceIDFromContext(ctx)
	meta.SpanFinisher = streamwrap.SpanFinisherFunc(func(o streamwrap.StreamOutcome) {
		// The model the PROVIDER reported, which is only knowable once chunks
		// have arrived — so it is stamped here rather than beside
		// gen_ai.request.model above. Without it the streaming span was the one
		// root span with no gen_ai.response.model, and a consumer comparing
		// requested against served model simply had no answer for streams.
		if o.Model != "" {
			finishSpan.SetAttribute(observability.AttrGenAIResponseModel, o.Model)
		}
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
					errors.New(o.ErrorMsg),
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

// recordStreamStartFailure finalizes a streaming request that never produced a
// channel: one refused before the plugin stage ran, and one whose start the
// pipeline could not complete. It runs the on_error stage so the request is
// recorded, counts the failure, stamps the span and dispatches the failed
// event; the caller returns the error and releases the plugin manager.
//
// providerName is the last target attempted, and is empty when none was — a
// plugin denial, or a model no configured target serves.
//
// pctx is nil when no plugins are configured; it is retired here, so the caller
// must not use it afterwards.
func (g *Gateway) recordStreamStartFailure(ctx context.Context, span observability.Span, obs observability.Provider, plugins *plugin.Manager, pctx *plugin.Context, providerName, model string, err error, start time.Time, hooksEnabled, obsEventsActive bool) {
	if pctx != nil {
		pctx.Error = err
		pctx.Target = providerName
		// The stream never started, so there is no first token and no cost —
		// but the attempt still took time, and a provider that took ten
		// seconds to refuse is a different fault from one that refused at once.
		pctx.Measurements = plugin.Measurements{DurationMs: float64(time.Since(start).Microseconds()) / 1000.0}
		plugins.RunOnError(ctx, pctx)
		plugin.PutContext(pctx)
	}
	// Providers that accept any model ID (openrouter, ollama, azure_openai, …)
	// let a raw client model reach this counter, so bound it.
	requestMetrics := metrics.ForRequest(providerName, g.metricModel(model))
	// A stream that never started still took time, and how long it took to
	// fail belongs in the same histogram as how long a success took —
	// otherwise the quantiles answer "how fast are the streams that worked".
	requestMetrics.Duration.Observe(time.Since(start).Seconds())
	requestMetrics.Error.Inc()
	recordProviderErrorCtx(ctx, providerName, err)
	span.SetError(err)
	if hooksEnabled || obsEventsActive {
		he := failedEventData(
			logger.TraceIDFromContext(ctx),
			providerName,
			model,
			err,
			time.Since(start),
			true,
		)
		g.dispatchRequestEvent(ctx, obs, hooksEnabled, obsEventsActive, he)
	}
}

// runBeforePluginsStream runs before-request plugins for the streaming path
// and finalizes bookkeeping on every path except "continue routing": a
// non-nil err means the caller must return (nil, err) immediately (metrics,
// plugin-context release, and plugin-manager release are already done); a
// non-nil early means the caller must return (responseStream(early), nil)
// immediately (success recording and release already done). Otherwise the
// returned pctx (nil if no plugins are configured, non-nil and still live
// otherwise) is what the rest of RouteStream continues to use.
func (g *Gateway) runBeforePluginsStream(ctx context.Context, span observability.Span, obs observability.Provider, plugins *plugin.Manager, releasePluginManager func(), req *providers.Request, start time.Time, hooksEnabled, obsEventsActive bool) (pctx *plugin.Context, early *providers.Response, err error) {
	if !plugins.HasPlugins() {
		releasePluginManager()
		return nil, nil, nil
	}

	pctx = g.newPluginContext(ctx, plugins, span, req)
	trace.WithRegion(ctx, "gateway.route_stream.plugins.before", func() {
		early, err = g.runBeforePlugins(ctx, plugins, pctx, req, start)
	})
	if err != nil {
		plugin.PutContext(pctx)
		releasePluginManager()
		// Same accounting the non-streaming path does for a rejected request:
		// it took time too, and the histogram is meant to answer "how fast is
		// the gateway", not "how fast are the requests that worked".
		h := metrics.ForRequest("", g.metricModel(req.Model))
		h.Duration.Observe(time.Since(start).Seconds())
		recordPluginAbort(h, err)
		return nil, nil, err
	}
	if early != nil {
		if early.Created == 0 {
			early.Created = time.Now().Unix()
		}
		g.recordCacheHit(ctx, span, obs, early, time.Since(start), true, hooksEnabled, obsEventsActive)
		plugin.PutContext(pctx)
		releasePluginManager()
		return nil, early, nil
	}
	return pctx, nil, nil
}
