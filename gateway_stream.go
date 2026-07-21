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
	"github.com/ferro-labs/ai-gateway/internal/redact"
	"github.com/ferro-labs/ai-gateway/internal/streamwrap"
	"github.com/ferro-labs/ai-gateway/observability"
	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// Streaming request path (RouteStream) plus its streaming provider-resolution
// and target-ordering helpers, the streaming latency/cost candidate types, and
// the generic target-list helpers.

// streamStartErrRedactor applies the default sensitive-data policies to a
// synchronous stream-start failure before it reaches hooks and observability
// exporters (events.HookEvent, not the span — span.SetError already redacts
// per the configured privacy level). This is exactly the 401-with-key-fragment
// path: an upstream error body can echo back part of the caller's own bearer
// token. A single package-level instance avoids recompiling the redaction
// regexes on every failed request, mirroring internal/redact's own
// defaultRedactor.
var streamStartErrRedactor = redact.DefaultRedactor()

// RouteStream runs before-request plugins then returns a metered streaming
// response channel. Provider resolution follows the configured strategy mode;
// when no configured target matches the model it falls back to any registered
// streaming-capable provider (matching Route's discovery-provider fallbacks).
// Synchronous stream-start failures are retried and, in fallback mode,
// advanced to the next target using the same per-target retry policy that
// /v1/chat/completions honors — before any channel is exposed to the caller.
// Once CompleteStream succeeds, nothing is retried or replayed. Target
// selection, each CompleteStream call, and the retry/backoff waits between
// them are bounded by Config.RequestTimeout, if configured; a stream that
// does start is never bounded by it once its channel is visible below — see
// startStreamWithStrategy and raceCompleteStream. Prometheus metrics and
// event hooks are emitted when the returned channel drains (matching the
// behaviour of Route for non-streaming).
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
	sp, providerName, rawCh, err := g.startStreamWithStrategy(startCtx, ctx, req)
	span.SetAttribute(observability.AttrGenAISystem, providerName)
	// Stamp the resolved target key (virtual key = provider name in this routing layer).
	if providerName != "" {
		span.SetAttribute(observability.AttrFerroRoutingTargetKey, providerName)
	}
	if err == nil && logging.Enabled(ctx, slog.LevelDebug) {
		logging.FromContext(ctx).Debug("stream request started", "model", req.Model, "provider", providerName)
	}
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
				streamStartErrRedactor.Redact(err.Error()),
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
		// Usage is always requested upstream so metering, cost, and the budget
		// plugin see real numbers; a caller that asked not to receive it just
		// does not get the chunk forwarded.
		SuppressUsageForClient: req.ClientStreamOptions != nil && !req.ClientStreamOptions.IncludeUsage,
	}
	if hooksEnabled {
		meta.PublishFn = g.publishEvent
	}
	if wrapped, ok := sp.(*cbProvider); ok {
		cb := wrapped.cb
		cbName := wrapped.name
		meta.CircuitBreakerOutcome = func(err error) {
			recordCircuitBreakerOutcome(ctx, cb, cbName, err)
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
		recordPluginAbort(metrics.ForRequest("", g.metricModel(req.Model)), err)
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

// startStreamWithStrategy tries configured, model-compatible streaming
// targets in strategy order. Fallback mode applies each target's retry policy
// (runTargetAttempts) and advances to the next target on synchronous setup
// failure; every other mode attempts only the first viable target, matching
// the single-shot semantics Route's non-Fallback strategies already have. If
// no configured target is even viable for the model (wrong model, not a
// StreamProvider, not registered) it falls back to any registered
// streaming-capable provider for the model — preserving v1.3.0's guarantee
// that a provider registered outside the target list stays reachable for the
// models it serves (see resolveFallbackStreamProviderLocked). A returned
// channel is never replayed.
//
// startCtx bounds this whole selection/retry phase only — it is what
// runTargetAttempts and strategies.WaitBeforeRetry check for expiry. streamCtx
// is the context every CompleteStream call actually runs on and must stay
// free of that deadline: a provider keeps reading its response body on
// whatever context it was called with for as long as the returned channel is
// alive, so a start-phase timeout attached to streamCtx would tear down an
// already-successful stream the moment the clock ran out.
func (g *Gateway) startStreamWithStrategy(startCtx, streamCtx context.Context, req providers.Request) (providers.StreamProvider, string, <-chan providers.StreamChunk, error) {
	g.mu.Lock()
	g.ensureCircuitBreakersLocked()
	g.ensureProviderLimitersLocked()
	g.mu.Unlock()

	orderedKeys, err := g.streamingTargetOrder(req)
	if err != nil {
		return nil, "", nil, err
	}
	g.mu.RLock()
	mode := g.config.Strategy.Mode
	g.mu.RUnlock()

	var (
		lastErr      error
		lastProvider string
		anyViable    bool
	)
	for _, key := range orderedKeys {
		g.mu.RLock()
		sp, ok := g.streamingProviderForTargetLocked(key, req.Model)
		g.mu.RUnlock()
		if !ok {
			continue
		}
		anyViable = true
		providerName := sp.Name()

		raw, attemptErr := g.attemptStreamStart(startCtx, streamCtx, key, sp, req)
		if attemptErr == nil {
			return sp, providerName, raw, nil
		}
		lastErr = fmt.Errorf("provider %s stream start: %w", key, attemptErr)
		lastProvider = providerName
		if mode != ModeFallback {
			break
		}
	}

	// No configured target even matches this model/streaming capability — try
	// any registered streaming-capable provider for it (v1.3.0 behaviour). This
	// runs regardless of strategy mode, exactly like the resolution-only
	// fallback it replaces: it is never reached when at least one configured
	// target was viable, so it never overrides a real fallback-mode failure
	// above with an unconfigured provider.
	if !anyViable {
		g.mu.RLock()
		name, sp, ok := g.resolveFallbackStreamProviderLocked(req.Model)
		g.mu.RUnlock()
		if ok {
			raw, attemptErr := g.attemptStreamStart(startCtx, streamCtx, name, sp, req)
			if attemptErr == nil {
				return sp, sp.Name(), raw, nil
			}
			lastErr = fmt.Errorf("provider %s stream start: %w", name, attemptErr)
			lastProvider = sp.Name()
		}
	}

	if lastErr != nil {
		return nil, lastProvider, nil, lastErr
	}
	return nil, "", nil, fmt.Errorf("%w: no streaming provider for %q", core.ErrNoCapableProvider, req.Model)
}

// attemptStreamStart runs the configured retry policy for one resolved
// candidate (g.runTargetAttempts), racing each try's CompleteStream call
// against startCtx via raceCompleteStream. Shared by the configured-target
// loop and the registry-fallback candidate in startStreamWithStrategy so both
// attempt a candidate identically.
func (g *Gateway) attemptStreamStart(startCtx, streamCtx context.Context, key string, sp providers.StreamProvider, req providers.Request) (<-chan providers.StreamChunk, error) {
	var raw <-chan providers.StreamChunk
	err := g.runTargetAttempts(startCtx, key, func(attemptCtx context.Context) error {
		var startErr error
		trace.WithRegion(attemptCtx, "gateway.route_stream.provider.start", func() {
			raw, startErr = raceCompleteStream(attemptCtx, streamCtx, sp, req)
		})
		if startErr == nil && raw == nil {
			return fmt.Errorf("provider %s returned a nil stream", key)
		}
		return startErr
	})
	return raw, err
}

// raceCompleteStream bounds only the wait for sp.CompleteStream to return. The
// call itself always runs on streamCtx; waitCtx is consulted solely to decide
// how long to keep waiting for a result. If waitCtx expires first, the attempt
// is abandoned and reported as a failure so runTargetAttempts can retry or
// fall back, while the abandoned call keeps running on streamCtx and resolves
// independently — its result, and any circuit-breaker bookkeeping
// CompleteStream performs, land whenever the provider actually answers.
//
// An abandoned attempt that later succeeds hands back a live channel no caller
// will ever read. Its producer would then block forever on the first send,
// holding the provider connection until streamCtx is cancelled, so the
// abandoning path drains that channel to completion instead of dropping it.
func raceCompleteStream(waitCtx, streamCtx context.Context, sp providers.StreamProvider, req providers.Request) (<-chan providers.StreamChunk, error) {
	type startResult struct {
		ch  <-chan providers.StreamChunk
		err error
	}
	done := make(chan startResult, 1)
	go func() {
		ch, err := sp.CompleteStream(streamCtx, req)
		done <- startResult{ch, err}
	}()
	select {
	case r := <-done:
		return r.ch, r.err
	case <-waitCtx.Done():
		go func() {
			if r := <-done; r.ch != nil {
				for range r.ch { //nolint:revive // drain so the provider's producer can finish
				}
			}
		}()
		return nil, context.Cause(waitCtx)
	}
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

// resolveFallbackStreamProviderLocked returns any registered provider that
// supports model via streaming, decorated with its own circuit breaker and
// concurrency limiter, for use when no configured target matches. Preserving
// this last-resort lookup is what keeps a model served by a
// registered-but-unlisted provider streaming (v1.3.0 behaviour predating
// per-target retry; see startStreamWithStrategy). Caller must hold g.mu (a
// read lock is sufficient).
func (g *Gateway) resolveFallbackStreamProviderLocked(model string) (string, providers.StreamProvider, bool) {
	name, fallback, ok := g.findStreamingProviderMatchByModelLocked(model)
	if !ok {
		return "", nil, false
	}
	if decorated, dok := decorateProvider(name, g.providers[name], g.circuitBreakers[name], g.limiters[name]).(providers.StreamProvider); dok {
		return name, decorated, true
	}
	return name, fallback, true
}

// streamingProviderForTargetLocked resolves the streaming-capable provider
// for a single configured target key, applying its circuit breaker and
// concurrency limiter decoration. Caller must hold g.mu (a read lock is
// sufficient).
func (g *Gateway) streamingProviderForTargetLocked(key, model string) (providers.StreamProvider, bool) {
	p, ok := g.providers[key]
	if !ok || !p.SupportsModel(model) {
		return nil, false
	}

	sp, ok := p.(providers.StreamProvider)
	if !ok {
		return nil, false
	}

	// Apply the circuit breaker and concurrency limit configured for this target.
	if decorated, ok := decorateProvider(key, p, g.circuitBreakers[key], g.limiters[key]).(providers.StreamProvider); ok {
		return decorated, true
	}
	return sp, true
}
