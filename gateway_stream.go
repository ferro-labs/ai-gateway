package aigateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/trace"
	"sync"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/authctx"
	"github.com/ferro-labs/ai-gateway/internal/circuitbreaker"
	"github.com/ferro-labs/ai-gateway/internal/events"
	"github.com/ferro-labs/ai-gateway/internal/logging"
	"github.com/ferro-labs/ai-gateway/internal/metrics"
	"github.com/ferro-labs/ai-gateway/internal/streamwrap"
	"github.com/ferro-labs/ai-gateway/observability"
	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
)

// Streaming request path (RouteStream) plus its streaming provider-resolution
// and target-ordering helpers, the streaming latency/cost candidate types, and
// the generic target-list helpers.

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
	pctx, early, err := g.runBeforePluginsStream(ctx, span, obs, plugins, releasePluginManager, &req, start, hooksEnabled, obsEventsActive)
	if err != nil {
		return nil, err
	}
	if early != nil {
		return responseStream(early), nil
	}

	// Resolve provider according to strategy mode.
	sp, err := g.resolveStreamOrError(ctx, span, plugins, pctx, releasePluginManager, req)
	if err != nil {
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
		// Providers that accept any model ID (openrouter, ollama, azure_openai, …)
		// let a raw client model reach this counter, so bound it.
		metrics.ForRequest(providerName, g.metricModel(req.Model)).Error.Inc()
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
		Provider: providerName,
		Model:    req.Model,
		// Model stays raw for cost lookup and event payloads; only the metric
		// label is bounded, mirroring the non-streaming path's use of the
		// provider-reported model.
		MetricModel:     g.metricModel(req.Model),
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

	pctx = plugin.NewContext(req)
	pctx.Span = span // per-plugin child spans nest under the request span.
	// Propagate the opaque key identifier so per-key plugins (rate-limit,
	// budget) can scope limits to the authenticated caller. The raw bearer
	// secret is never exposed here — only the stable APIKey.ID.
	if keyID, ok := authctx.KeyID(ctx); ok {
		pctx.Metadata["api_key"] = keyID
	}
	trace.WithRegion(ctx, "gateway.route_stream.plugins.before", func() {
		early, err = g.runBeforePlugins(ctx, plugins, pctx, req)
	})
	if err != nil {
		plugin.PutContext(pctx)
		releasePluginManager()
		metrics.ForRequest("", g.metricModel(req.Model)).Rejected.Inc()
		return nil, nil, err
	}
	if early != nil {
		if early.Created == 0 {
			early.Created = time.Now().Unix()
		}
		g.recordSuccess(ctx, span, obs, early, time.Since(start), true, hooksEnabled, obsEventsActive)
		plugin.PutContext(pctx)
		releasePluginManager()
		return nil, early, nil
	}
	return pctx, nil, nil
}

// resolveStreamOrError resolves the streaming-capable provider for req under
// the gateway lock. On failure (resolution error, or no streaming-capable
// provider found) it finalizes bookkeeping (span error, plugin error hook if
// pctx is live, plugin-context release, plugin-manager release) and returns
// a non-nil error — the caller must return (nil, err) immediately, same as
// every other terminal error path in RouteStream.
func (g *Gateway) resolveStreamOrError(ctx context.Context, span observability.Span, plugins *plugin.Manager, pctx *plugin.Context, releasePluginManager func(), req providers.Request) (providers.StreamProvider, error) {
	g.mu.Lock()
	g.ensureCircuitBreakersLocked()
	g.mu.Unlock()

	fail := func(err error) (providers.StreamProvider, error) {
		span.SetError(err)
		if pctx != nil {
			pctx.Error = err
			plugins.RunOnError(ctx, pctx)
			plugin.PutContext(pctx)
			releasePluginManager()
		}
		return nil, err
	}

	orderedKeys, orderErr := g.streamingTargetOrder(req)
	if orderErr != nil {
		return fail(orderErr)
	}

	g.mu.RLock()
	sp := g.resolveStreamProviderFromKeysLocked(orderedKeys, req.Model)
	g.mu.RUnlock()

	if sp == nil {
		return fail(fmt.Errorf("no streaming-capable provider found for model: %s", req.Model))
	}
	return sp, nil
}

// streamingTargetOrder resolves the strategy — the same object Route executes —
// and asks it for the streaming target order, so both paths share one ordering
// implementation. A getStrategy error surfaces identically on both paths; for
// ValidateConfig-passing gateways getStrategy does not error here.
func (g *Gateway) streamingTargetOrder(req providers.Request) ([]string, error) {
	s, err := g.getStrategy()
	if err != nil {
		return nil, err
	}
	return s.SelectTargets(req)
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

// resolveStreamProviderFromKeysLocked walks orderedKeys and returns the first
// streaming-capable, model-supporting provider whose circuit is not open. If
// every ordered target has an open circuit it returns the last such target so
// the caller still attempts it (and surfaces the open-circuit error). Failing
// all ordered targets it falls back to any registered streaming-capable provider
// for the model. Returns nil when nothing matches. Caller must hold g.mu.
//
// The breaker is probed with State() (non-consuming) rather than Allow() here:
// the single Allow() inside cbProvider.CompleteStream is the one probe per
// streaming request, matching the non-streaming path where cbProvider.Complete
// performs the sole Allow(). Consuming a half-open permit at resolve time would
// leave no permit for the actual stream, so a recovering provider could never be
// probed by a streaming request.
func (g *Gateway) resolveStreamProviderFromKeysLocked(orderedKeys []string, model string) providers.StreamProvider {
	var openCircuitTarget providers.StreamProvider
	for _, key := range orderedKeys {
		sp, ok := g.streamingProviderForTargetLocked(key, model)
		if !ok {
			continue
		}
		if wrapped, isCB := sp.(*cbProvider); isCB && wrapped.cb.State() == circuitbreaker.StateOpen {
			openCircuitTarget = sp
			continue
		}
		return sp
	}
	if openCircuitTarget != nil {
		return openCircuitTarget
	}

	// Fallback: any registered provider that supports this model and streaming.
	name, fallback, ok := g.findStreamingProviderMatchByModelLocked(model)
	if !ok {
		return nil
	}
	if cb, hasCB := g.circuitBreakers[name]; hasCB {
		return &cbProvider{Provider: g.providers[name], cb: cb, name: name}
	}
	return fallback
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
