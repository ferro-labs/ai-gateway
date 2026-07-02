package aigateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"runtime/trace"
	"sort"
	"sync"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/authctx"
	"github.com/ferro-labs/ai-gateway/internal/circuitbreaker"
	"github.com/ferro-labs/ai-gateway/internal/events"
	"github.com/ferro-labs/ai-gateway/internal/logging"
	"github.com/ferro-labs/ai-gateway/internal/metrics"
	"github.com/ferro-labs/ai-gateway/internal/streamwrap"
	"github.com/ferro-labs/ai-gateway/models"
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
	var pctx *plugin.Context
	if plugins.HasPlugins() {
		pctx = plugin.NewContext(&req)
		// Propagate the opaque key identifier so per-key plugins (rate-limit,
		// budget) can scope limits to the authenticated caller. The raw bearer
		// secret is never exposed here — only the stable APIKey.ID.
		if keyID, ok := authctx.KeyID(ctx); ok {
			pctx.Metadata["api_key"] = keyID
		}
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
			if early.Created == 0 {
				early.Created = time.Now().Unix()
			}
			g.recordSuccess(ctx, span, obs, early, time.Since(start), true, hooksEnabled, obsEventsActive)
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
		return targetKeys(targets), nil
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
		return targetKeys(targets), nil
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
		return targetKeys(targets), nil
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
		return targetKeys(targets), nil
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
