package aigateway

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/binary"
	"fmt"
	"log/slog"
	"maps"
	"sort"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/authctx"
	"github.com/ferro-labs/ai-gateway/internal/logging"
	"github.com/ferro-labs/ai-gateway/internal/metrics"
	"github.com/ferro-labs/ai-gateway/internal/redact"
	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/observability"
	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// The non-chat surfaces this file routes. The name reaches operators as the
// span's operation and as a metrics label, so it is fixed here rather than
// spelled out at each of its call sites.
const (
	surfaceEmbeddings = "embeddings"
	surfaceImages     = "images"
)

// Gateway model alias resolution, the multi-modal (embedding / image) routing
// endpoints, and background model auto-discovery.

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

// runSurfaceGovernance runs the before/after/error plugin pipeline around a
// non-chat surface (embeddings, images) so per-key governance plugins — budget,
// rate-limit — apply uniformly across every OpenAI surface, not just chat.
//
// It reuses the frozen plugin.Manager/Context without inventing a new lifecycle
// (plugins architecture §2): the Context carries no chat Request, so content
// plugins that key off Request.Messages guard nil and no-op; surface identity
// and, on success, normalized token usage flow through Metadata — the sanctioned
// additive channel (plugins architecture D8). call performs the provider request
// and returns the token usage to account for, or nil when the surface reports
// none (e.g. image generation). span, when non-nil, is the surface's request
// root span; plugin invocations open per-plugin child spans through it, same
// as chat's runBeforePlugins.
func (g *Gateway) runSurfaceGovernance(ctx context.Context, surface string, span observability.Span, call func(context.Context) (*providers.Usage, error)) error {
	g.mu.RLock()
	plugins := g.plugins
	release := acquirePluginManager(plugins)
	g.mu.RUnlock()
	defer release()

	if !plugins.HasPlugins() {
		_, err := call(ctx)
		return err
	}

	pctx := plugin.NewContext(nil)
	pctx.Span = span
	defer plugin.PutContext(pctx)
	pctx.Metadata["surface"] = surface
	if keyID, ok := authctx.KeyID(ctx); ok {
		pctx.Metadata["api_key"] = keyID
	}

	if err := plugins.RunBefore(ctx, pctx); err != nil {
		return err
	}
	// A before-plugin can still set pctx.Reject (honoured above via the
	// RunBefore error) or pctx.Skip (stops the remaining before-stage
	// plugins). What it CANNOT do here is substitute a cached response the
	// way response-cache does for chat: pctx.Response is providers.Response,
	// the chat-shaped envelope, with no field able to hold an
	// EmbeddingResponse or ImageResponse. So the provider call below always
	// still runs after a before-stage Skip on this surface — Reject is the
	// supported way to stop a request here, not Skip.

	usage, err := call(ctx)
	if err != nil {
		pctx.Error = err
		plugins.RunOnError(ctx, pctx)
		return err
	}
	if usage != nil {
		pctx.Metadata["usage"] = *usage
	}
	// Explicit after_request signal so stage detection does not depend on token
	// usage — image responses carry none. See budget.requestCompleted.
	pctx.Metadata["completed"] = true
	return plugins.RunAfter(ctx, pctx)
}

// Embed routes an embedding request across configured, capable targets using
// the gateway strategy (honouring strategy.mode, retry, and fallback — see
// surfaceTargetOrder), under the shared governance pipeline. It emits the same
// class of observability signal as chat's Route: a request span, Prometheus
// request metrics, cost accounting, and a completed/failed lifecycle event.
func (g *Gateway) Embed(ctx context.Context, req providers.EmbeddingRequest) (*providers.EmbeddingResponse, error) {
	log := logging.FromContext(ctx)
	start := time.Now()
	hooksEnabled := g.hasHooks()

	g.mu.RLock()
	requestTimeout := g.config.RequestTimeout
	strategyMode := string(g.config.Strategy.Mode)
	obs := g.obs
	obsEventsActive := g.obsEventsActive
	g.mu.RUnlock()
	ctx, cancelDeadline := withRequestDeadline(ctx, requestTimeout)
	defer cancelDeadline()

	ctx, span := obs.StartRequestSpan(ctx, observability.RequestAttrs{
		Operation:       surfaceEmbeddings,
		RequestModel:    req.Model,
		TraceID:         logging.TraceIDFromContext(ctx),
		RoutingStrategy: strategyMode,
	})
	defer span.End()

	// Resolve model alias so embedding endpoints honour the same aliases as chat.
	req.Model = g.resolveModelAlias(req.Model)

	var resp *providers.EmbeddingResponse
	var providerName string
	err := g.runSurfaceGovernance(ctx, surfaceEmbeddings, span, func(ctx context.Context) (*providers.Usage, error) {
		var routeErr error
		resp, providerName, routeErr = g.routeEmbedding(ctx, req)
		if routeErr != nil {
			return nil, routeErr
		}
		return &providers.Usage{PromptTokens: resp.Usage.PromptTokens, TotalTokens: resp.Usage.TotalTokens}, nil
	})
	latency := time.Since(start)
	if err != nil {
		safeErr := g.recordSurfaceError(ctx, span, obs, providerName, req.Model, err, latency, hooksEnabled, obsEventsActive)
		log.Error("embedding request failed", "model", req.Model, "error", safeErr)
		return nil, err
	}

	g.recordSurfaceSuccess(ctx, span, obs, providerName, resp.Model,
		models.Usage{PromptTokens: resp.Usage.PromptTokens}, latency, hooksEnabled, obsEventsActive)

	log.Info("embedding request completed", "model", resp.Model, "tokens", resp.Usage.TotalTokens)
	return resp, nil
}

// GenerateImage routes an image request across configured, capable targets
// using the gateway strategy (see surfaceTargetOrder), under the shared
// governance pipeline. Image responses carry no token usage, so the budget
// plugin gates but does not cost them via the Metadata["usage"] channel — cost
// accounting here instead comes from models.Calculate against the returned
// image count, the same way chat's cost accounting works from token counts.
func (g *Gateway) GenerateImage(ctx context.Context, req providers.ImageRequest) (*providers.ImageResponse, error) {
	log := logging.FromContext(ctx)
	start := time.Now()
	hooksEnabled := g.hasHooks()

	g.mu.RLock()
	requestTimeout := g.config.RequestTimeout
	strategyMode := string(g.config.Strategy.Mode)
	obs := g.obs
	obsEventsActive := g.obsEventsActive
	g.mu.RUnlock()
	ctx, cancelDeadline := withRequestDeadline(ctx, requestTimeout)
	defer cancelDeadline()

	ctx, span := obs.StartRequestSpan(ctx, observability.RequestAttrs{
		Operation:       "images.generate",
		RequestModel:    req.Model,
		TraceID:         logging.TraceIDFromContext(ctx),
		RoutingStrategy: strategyMode,
	})
	defer span.End()

	// Resolve model alias so image endpoints honour the same aliases as chat.
	req.Model = g.resolveModelAlias(req.Model)

	var resp *providers.ImageResponse
	var providerName string
	err := g.runSurfaceGovernance(ctx, surfaceImages, span, func(ctx context.Context) (*providers.Usage, error) {
		var routeErr error
		resp, providerName, routeErr = g.routeImage(ctx, req)
		if routeErr != nil {
			return nil, routeErr
		}
		return nil, nil // image responses carry no token usage
	})
	latency := time.Since(start)
	if err != nil {
		safeErr := g.recordSurfaceError(ctx, span, obs, providerName, req.Model, err, latency, hooksEnabled, obsEventsActive)
		log.Error("image generation request failed", "model", req.Model, "error", safeErr)
		return nil, err
	}

	g.recordSurfaceSuccess(ctx, span, obs, providerName, req.Model,
		models.Usage{ImageCount: len(resp.Data)}, latency, hooksEnabled, obsEventsActive)

	log.Info("image generation request completed", "model", req.Model, "images", len(resp.Data))
	return resp, nil
}

// recordSurfaceSuccess finalizes a completed embeddings/images request: emits
// Prometheus request metrics, computes cost via models.Calculate, stamps the
// span, and dispatches the completed lifecycle event. It is the non-chat
// counterpart of recordSuccess (gateway_route.go), reusing its lifecycle-event
// plumbing (dispatchRequestEvent, completedEventData) rather than duplicating
// it. It takes a models.Usage directly — rather than chat's providers.Usage —
// because image cost depends on ImageCount, a field providers.Usage/Response
// has no room for. providerName is the resolved provider's canonical Name(),
// used for both the cost/catalog lookup key and the public routing-target-key
// span attribute; in this routing layer a target's virtual_key and its
// provider's registered Name() are the same string (see RegisterProvider).
func (g *Gateway) recordSurfaceSuccess(ctx context.Context, span observability.Span, obs observability.Provider, providerName, model string, usage models.Usage, latency time.Duration, hooksEnabled, obsEventsActive bool) {
	// Bound the metric label but keep the raw model for the cost lookup below.
	// Providers that accept any model ID (openrouter, ollama, azure_openai, …)
	// echo the caller's string back on success, so an unbounded label here would
	// let a client mint a new time series per request. The streaming path bounds
	// its label the same way.
	requestMetrics := metrics.ForRequest(providerName, g.metricModel(model))
	requestMetrics.Duration.Observe(latency.Seconds())
	requestMetrics.Success.Inc()
	requestMetrics.TokensIn.Add(float64(usage.PromptTokens))
	requestMetrics.TokensOut.Add(float64(usage.CompletionTokens))

	g.mu.RLock()
	catalog := g.catalog
	g.mu.RUnlock()
	cost := models.Calculate(catalog, providerName+"/"+model, usage)
	if cost.TotalUSD > 0 {
		requestMetrics.CostUSD.Add(cost.TotalUSD)
	}

	span.SetAttribute(observability.AttrGenAISystem, providerName)
	span.SetAttribute(observability.AttrGenAIResponseModel, model)
	if providerName != "" {
		span.SetAttribute(observability.AttrFerroRoutingTargetKey, providerName)
	}
	span.SetTokens(usage.PromptTokens, usage.CompletionTokens, usage.ReasoningTokens)
	span.SetCost(observability.CostBreakdown{
		TotalUSD:      cost.TotalUSD,
		InputUSD:      cost.InputUSD,
		OutputUSD:     cost.OutputUSD,
		CacheReadUSD:  cost.CacheReadUSD,
		CacheWriteUSD: cost.CacheWriteUSD,
		ReasoningUSD:  cost.ReasoningUSD,
		ModelFound:    cost.ModelFound,
	})

	if hooksEnabled || obsEventsActive {
		he := completedEventData(
			logging.TraceIDFromContext(ctx),
			providerName,
			model,
			latency,
			false,
			usage.PromptTokens,
			usage.CompletionTokens,
			cost,
		)
		g.dispatchRequestEvent(ctx, obs, hooksEnabled, obsEventsActive, he)
	}
}

// recordSurfaceError finalizes a failed embeddings/images request: increments
// the Prometheus error counter, stamps the span with the error, and
// dispatches the failed lifecycle event. It is the non-chat counterpart of
// routeError (gateway_route.go), reusing its lifecycle-event plumbing
// (dispatchRequestEvent, failedEventData) rather than duplicating it.
// providerName is "" when routing never resolved any target, matching chat's
// empty-provider error label (see Route's own routeError call on a strategy
// Execute failure). It returns the redacted message for the caller's own log
// line; the lifecycle event is redacted independently by events.FailedRequest.
func (g *Gateway) recordSurfaceError(ctx context.Context, span observability.Span, obs observability.Provider, providerName, model string, err error, latency time.Duration, hooksEnabled, obsEventsActive bool) string {
	metrics.ForRequest(providerName, g.metricModel(model)).Error.Inc()
	span.SetError(err)
	safeErr := redact.ErrorMessage(err)

	if hooksEnabled || obsEventsActive {
		he := failedEventData(
			logging.TraceIDFromContext(ctx),
			providerName,
			model,
			safeErr,
			latency,
			false,
		)
		g.dispatchRequestEvent(ctx, obs, hooksEnabled, obsEventsActive, he)
	}
	return safeErr
}

// surfaceTargetOrder resolves the candidate target keys for one embeddings/
// images request, honouring strategy.mode the same way chat's getStrategy
// does. mode is returned so routeEmbedding/routeImage know whether to advance
// to the next target on failure (ModeFallback) or stop at the first attempt.
func (g *Gateway) surfaceTargetOrder(model, surface string, usage models.Usage) ([]string, StrategyMode, error) {
	g.mu.Lock()
	g.ensureCircuitBreakersLocked()
	g.ensureProviderLimitersLocked()
	mode := g.config.Strategy.Mode
	targets := append([]Target(nil), g.config.Targets...)
	if mode == ModeLatency || mode == ModeCostOptimized || mode == ModeLoadBalance {
		// Snapshot everything ranking needs, then release g.mu before doing any
		// of the actual work. rankConfiguredSurfaceTargets loops every target
		// doing latency/catalog lookups and, for ModeLoadBalance, reads the
		// system CSPRNG (secureRandomUnit) — none of that should run while
		// every chat request, admin config read, and provider registration
		// blocks on this single global write lock. Mirrors getStrategy's
		// snapshot-then-release pattern (gateway_strategy.go): map values
		// (Provider) are themselves immutable references, and g.catalog is
		// replaced wholesale rather than mutated in place (see
		// refreshCatalog), so a plain reference copy is safe without cloning.
		providerSnap := maps.Clone(g.providers)
		catalog := g.catalog
		unpricedStrategy := g.config.Strategy.UnpricedStrategy
		g.mu.Unlock()

		keys, err := g.rankConfiguredSurfaceTargets(targets, providerSnap, catalog, unpricedStrategy, model, surface, usage, mode)
		return keys, mode, err
	}
	g.mu.Unlock()

	if mode == ModeContentBased {
		// Content rules only look at req.Messages (prompt_contains /
		// prompt_not_contains / prompt_regex), and this surface has none —
		// Embed/GenerateImage requests carry no chat messages. prompt_contains
		// and prompt_regex correctly find no match against zero messages, but
		// strategies.ContentBased's prompt_not_contains rule is
		// `!anyUserMessageContains(...)`, which is vacuously TRUE over an
		// empty message set: it would match every embeddings/image request
		// and win routing outright, regardless of what the rule actually
		// says. No content rule can be meaningfully evaluated without a
		// prompt, so route in configured target order instead — exactly what
		// strategy.SelectTargets already falls back to when no rule matches.
		keys := make([]string, len(targets))
		for i, t := range targets {
			keys[i] = t.VirtualKey
		}
		return keys, mode, nil
	}

	// Single, Fallback, Conditional, and ABTest key off req.Model (or nothing
	// at all) — none of them need a prompt — so the shared strategy can
	// resolve them exactly as it does for chat.
	strategy, err := g.getStrategy()
	if err != nil {
		return nil, "", err
	}
	keys, err := strategy.SelectTargets(providers.Request{Model: model})
	if err != nil {
		return nil, "", err
	}
	return keys, mode, nil
}

type surfaceRankCandidate struct {
	key        string
	latency    time.Duration
	hasLatency bool
	cost       models.CostResult
	priced     bool
	weight     float64
}

// rankConfiguredSurfaceTargets orders configured targets for ModeLatency,
// ModeCostOptimized, and ModeLoadBalance. It takes its inputs as arguments
// instead of reading g.config/g.providers/g.catalog directly (g.latencyTracker
// is the one exception — its own mutex makes it safe to read unlocked) so
// surfaceTargetOrder can call it after releasing g.mu rather than holding the
// gateway's single global write lock across ranking, latency/catalog lookups,
// and a crypto/rand syscall.
func (g *Gateway) rankConfiguredSurfaceTargets(targets []Target, providerSnap map[string]providers.Provider, catalog models.Catalog, unpricedStrategy, model, surface string, usage models.Usage, mode StrategyMode) ([]string, error) {
	candidates := make([]surfaceRankCandidate, 0, len(targets))
	for _, target := range targets {
		p, ok := providerSnap[target.VirtualKey]
		if !ok || !p.SupportsModel(model) || !providerSupportsSurface(p, surface) {
			continue
		}
		candidate := surfaceRankCandidate{key: target.VirtualKey, weight: target.Weight}
		if mode == ModeLatency {
			candidate.latency, candidate.hasLatency = g.latencyTracker.Stats(target.VirtualKey)
		} else {
			modelKey := p.Name() + "/" + model
			candidate.cost = models.Calculate(catalog, modelKey, usage)
			candidate.priced = surfaceHasPrice(catalog, modelKey, surface)
		}
		candidates = append(candidates, candidate)
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	switch mode {
	case ModeLoadBalance:
		// Weight only providers that implement this surface. Running the generic
		// load-balancer first would let a chat-only target distort which embedding
		// or image provider is selected even though it can never serve the call.
		totalWeight := 0.0
		for _, candidate := range candidates {
			totalWeight += surfaceWeight(candidate.weight)
		}
		randomUnit, err := secureRandomUnit()
		if err != nil {
			return nil, fmt.Errorf("select load-balanced surface target: %w", err)
		}
		pick := randomUnit * totalWeight
		start := 0
		for i, candidate := range candidates {
			pick -= surfaceWeight(candidate.weight)
			if pick < 0 {
				start = i
				break
			}
		}
		rotated := make([]surfaceRankCandidate, 0, len(candidates))
		rotated = append(rotated, candidates[start:]...)
		rotated = append(rotated, candidates[:start]...)
		candidates = rotated
	case ModeLatency:
		// Cold targets stay ahead of sampled ones so they receive a profiling
		// request; within each group declared target order is the stable tie-break.
		sort.SliceStable(candidates, func(i, j int) bool {
			if candidates[i].hasLatency != candidates[j].hasLatency {
				return !candidates[i].hasLatency
			}
			return candidates[i].latency < candidates[j].latency
		})
	default:
		priced := make([]surfaceRankCandidate, 0, len(candidates))
		unpriced := make([]surfaceRankCandidate, 0, len(candidates))
		for _, candidate := range candidates {
			if candidate.priced {
				priced = append(priced, candidate)
			} else {
				unpriced = append(unpriced, candidate)
			}
		}
		sort.SliceStable(priced, func(i, j int) bool { return priced[i].cost.TotalUSD < priced[j].cost.TotalUSD })
		switch unpricedStrategy {
		case unpricedStrategySkip:
			if len(priced) == 0 {
				return nil, fmt.Errorf("no priced provider supports model %s", model)
			}
			candidates = priced
		case unpricedStrategyAllow:
			// Match the chat cost strategy: unpriced candidates participate at
			// estimated zero cost, while explicit zero prices remain priced.
			ordered := make([]surfaceRankCandidate, 0, len(candidates))
			ordered = append(ordered, unpriced...)
			ordered = append(ordered, priced...)
			candidates = ordered
		default:
			if len(priced) > 0 {
				candidates = priced
			} else {
				candidates = unpriced
			}
		}
	}

	keys := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		keys = append(keys, candidate.key)
	}
	return keys, nil
}

// secureRandomUnit returns a uniformly distributed value in [0, 1). Routing
// selection is externally observable, so use the system CSPRNG instead of a
// predictable process-local pseudo-random stream. Entropy failure is surfaced
// rather than silently biasing all traffic toward the first target.
//
// Only ever called from rankConfiguredSurfaceTargets, which surfaceTargetOrder
// invokes AFTER releasing g.mu — so this syscall never runs while holding the
// gateway's single global write lock.
func secureRandomUnit() (float64, error) {
	var randomBytes [8]byte
	if _, err := cryptorand.Read(randomBytes[:]); err != nil {
		return 0, err
	}
	const mantissaBits = 53
	value := binary.LittleEndian.Uint64(randomBytes[:]) >> (64 - mantissaBits)
	return float64(value) / float64(uint64(1)<<mantissaBits), nil
}

func surfaceWeight(weight float64) float64 {
	if weight <= 0 {
		return 1
	}
	return weight
}

func surfaceHasPrice(catalog models.Catalog, modelKey, surface string) bool {
	model, ok := catalog.GetForPricing(modelKey)
	if !ok {
		return false
	}
	switch surface {
	case surfaceEmbeddings:
		return model.Mode == models.ModeEmbedding && model.Pricing.EmbeddingPerMTokens != nil
	case surfaceImages:
		return model.Mode == models.ModeImage && model.Pricing.ImagePerTile != nil
	default:
		return false
	}
}

func providerSupportsSurface(p providers.Provider, surface string) bool {
	switch surface {
	case surfaceEmbeddings:
		_, ok := p.(providers.EmbeddingProvider)
		return ok
	case surfaceImages:
		_, ok := p.(providers.ImageProvider)
		return ok
	default:
		return false
	}
}

// routeEmbedding tries configured, model-compatible embedding targets in
// strategy order, applying each target's retry policy and — in fallback mode —
// advancing to the next target on failure (routeSurfaceTarget /
// runTargetAttempts). If no configured target is even viable for the model
// (wrong model, not an EmbeddingProvider, not registered) it falls back to any
// registered embedding-capable provider for it, preserving v1.3.0's guarantee
// that a provider registered outside the target list stays reachable for the
// models it serves — mirroring startStreamWithStrategy's use of
// resolveFallbackStreamProviderLocked for the streaming surface.
func (g *Gateway) routeEmbedding(ctx context.Context, req providers.EmbeddingRequest) (*providers.EmbeddingResponse, string, error) {
	keys, mode, err := g.surfaceTargetOrder(req.Model, surfaceEmbeddings, models.Usage{PromptTokens: 1})
	if err != nil {
		return nil, "", err
	}
	var lastErr error
	anyViable := false
	for _, key := range keys {
		g.mu.RLock()
		p, ok := g.providers[key]
		ep, capable := p.(providers.EmbeddingProvider)
		g.mu.RUnlock()
		if !ok || !capable || !p.SupportsModel(req.Model) {
			continue
		}
		anyViable = true
		providerName := p.Name()

		resp, callErr := routeSurfaceTarget(ctx, g, key, func(callCtx context.Context) (*providers.EmbeddingResponse, error) {
			return ep.Embed(callCtx, req)
		})
		if callErr == nil {
			return resp, providerName, nil
		}
		lastErr = fmt.Errorf("embedding target %s: %w", key, callErr)
		if mode != ModeFallback {
			return nil, providerName, lastErr
		}
	}

	if !anyViable {
		g.mu.RLock()
		name, ep, ok := g.findEmbeddingProviderByModelLocked(req.Model)
		g.mu.RUnlock()
		if ok {
			resp, callErr := routeSurfaceTarget(ctx, g, name, func(callCtx context.Context) (*providers.EmbeddingResponse, error) {
				return ep.Embed(callCtx, req)
			})
			if callErr == nil {
				return resp, name, nil
			}
			return nil, name, fmt.Errorf("embedding target %s: %w", name, callErr)
		}
	}

	if lastErr != nil {
		return nil, "", lastErr
	}
	return nil, "", fmt.Errorf("%w: no embedding provider for %q", core.ErrNoCapableProvider, req.Model)
}

// routeImage is routeEmbedding's counterpart for image generation; see its
// doc comment for the shared retry/fallback/registry-fallback shape.
func (g *Gateway) routeImage(ctx context.Context, req providers.ImageRequest) (*providers.ImageResponse, string, error) {
	imageCount := 1
	if req.N != nil && *req.N > 0 {
		imageCount = *req.N
	}
	keys, mode, err := g.surfaceTargetOrder(req.Model, surfaceImages, models.Usage{ImageCount: imageCount})
	if err != nil {
		return nil, "", err
	}
	var lastErr error
	anyViable := false
	for _, key := range keys {
		g.mu.RLock()
		p, ok := g.providers[key]
		ip, capable := p.(providers.ImageProvider)
		g.mu.RUnlock()
		if !ok || !capable || !p.SupportsModel(req.Model) {
			continue
		}
		anyViable = true
		providerName := p.Name()

		resp, callErr := routeSurfaceTarget(ctx, g, key, func(callCtx context.Context) (*providers.ImageResponse, error) {
			return ip.GenerateImage(callCtx, req)
		})
		if callErr == nil {
			return resp, providerName, nil
		}
		lastErr = fmt.Errorf("image target %s: %w", key, callErr)
		if mode != ModeFallback {
			return nil, providerName, lastErr
		}
	}

	if !anyViable {
		g.mu.RLock()
		name, ip, ok := g.findImageProviderByModelLocked(req.Model)
		g.mu.RUnlock()
		if ok {
			resp, callErr := routeSurfaceTarget(ctx, g, name, func(callCtx context.Context) (*providers.ImageResponse, error) {
				return ip.GenerateImage(callCtx, req)
			})
			if callErr == nil {
				return resp, name, nil
			}
			return nil, name, fmt.Errorf("image target %s: %w", name, callErr)
		}
	}

	if lastErr != nil {
		return nil, "", lastErr
	}
	return nil, "", fmt.Errorf("%w: no image generation provider for %q", core.ErrNoCapableProvider, req.Model)
}

// routeSurfaceTarget applies the shared retry, circuit-breaker, concurrency,
// and latency-accounting lifecycle for a single non-chat target — the
// embeddings/images counterpart of chat's decorateProvider + strategy.Execute
// path, reusing the same runTargetAttempts (gateway_retry.go) so backoff stays
// normalised in exactly one place.
func routeSurfaceTarget[T any](ctx context.Context, g *Gateway, key string, call func(context.Context) (*T, error)) (*T, error) {
	var response *T
	started := time.Now()
	err := g.runTargetAttempts(ctx, key, func(attemptCtx context.Context) error {
		return g.withTargetBreaker(attemptCtx, key, func(breakerCtx context.Context) error {
			return g.withTargetSlot(breakerCtx, key, func(slotCtx context.Context) error {
				var callErr error
				response, callErr = call(slotCtx)
				return callErr
			})
		})
	})
	if err == nil {
		g.latencyTracker.Record(key, time.Since(started))
	}
	return response, err
}

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
			log.Error("model discovery failed", "provider", name, "error", redact.ErrorMessage(err))
			continue
		}
		g.mu.Lock()
		g.discoveredModels[name] = models
		g.rebuildModelIndexesLocked()
		g.mu.Unlock()
		log.Info("model discovery completed", "provider", name, "models", len(models))
	}
}
