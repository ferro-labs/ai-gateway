package aigateway

import (
	cryptorand "crypto/rand"
	"encoding/binary"
	"fmt"
	"maps"
	"sort"
	"time"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// surfaceTargetOrder resolves the candidate target keys for one embeddings/
// images request, honouring strategy.mode the same way chat's getStrategy does.
//
// Order is all it decides. Whether the walk advances past a failed candidate is
// the pipeline's question, answered once for every surface by planFor, so a
// strategy that picks differently cannot also fall back differently.
func (g *Gateway) surfaceTargetOrder(model, surface string, usage models.Usage) ([]string, error) {
	g.mu.Lock()
	g.ensureCircuitBreakersLocked()
	g.ensureProviderLimitersLocked()
	mode := g.config.Strategy.Mode
	targets := append([]config.Target(nil), g.config.Targets...)
	if mode == config.ModeLatency || mode == config.ModeCostOptimized || mode == config.ModeLoadBalance {
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
		// The model index is snapshotted HERE, under the same lock as the
		// provider map, rather than read lock-free inside the ranking: an index
		// paired with a different generation of that map can rank a target the
		// map no longer holds. Same rule routingSnapshot exists to enforce.
		modelIndex := g.modelIndex.exactProviders
		catalog := g.catalog
		unpricedStrategy := g.config.Strategy.UnpricedStrategy
		g.mu.Unlock()

		return g.rankConfiguredSurfaceTargets(targets, providerSnap, modelIndex, catalog, unpricedStrategy, model, surface, usage, mode)
	}
	g.mu.Unlock()

	if mode == config.ModeContentBased {
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
		return keys, nil
	}

	// Single, Fallback, Conditional, and ABTest key off req.Model (or nothing
	// at all) — none of them need a prompt — so the shared strategy can
	// resolve them exactly as it does for chat.
	strategy, err := g.getStrategy()
	if err != nil {
		return nil, err
	}
	return strategy.SelectTargets(providers.Request{Model: model})
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
// It asks the routing index who serves the model, exactly as resolveTarget
// does, rather than the provider's own SupportsModel. Ranking on a wider answer
// than the one that admits the call is worse than not ranking at all under a
// non-fallback mode: the walk commits to the top-ranked target, so a target
// ranked here and rejected there answers 404 while a capable one sits behind it.
func (g *Gateway) rankConfiguredSurfaceTargets(targets []config.Target, providerSnap map[string]providers.Provider, modelIndex map[string][]string, catalog models.Catalog, unpricedStrategy, model, surface string, usage models.Usage, mode config.StrategyMode) ([]string, error) {
	candidates := make([]surfaceRankCandidate, 0, len(targets))
	for _, target := range targets {
		p, ok := providerSnap[target.VirtualKey]
		if !ok || !providerSupportsSurface(p, surface) {
			continue
		}
		owns, claimed := ownsModel(modelIndex, target.VirtualKey, model)
		if !owns && (claimed || !servesAnyModel(p)) {
			continue
		}
		candidate := surfaceRankCandidate{key: target.VirtualKey, weight: target.Weight}
		if mode == config.ModeLatency {
			candidate.latency, candidate.hasLatency = g.latencyTracker.Stats(target.VirtualKey)
		} else {
			modelKey := canonicalProviderName(p) + "/" + model
			candidate.cost = models.Calculate(catalog, modelKey, usage)
			candidate.priced = surfaceHasPrice(catalog, modelKey, surface)
		}
		candidates = append(candidates, candidate)
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	switch mode {
	case config.ModeLoadBalance:
		// Weight only providers that implement this surface. Running the generic
		// load-balancer first would let a chat-only target distort which embedding
		// or image provider is selected even though it can never serve the call.
		var totalWeight float64
		candidates, totalWeight = activeSurfaceCandidates(candidates, anyWeightExpressed(targets))
		if len(candidates) == 0 {
			// Every capable target is drained. That is an answer, not a fault:
			// the operator said take no traffic, so the request is refused
			// rather than quietly routed to a target already out of service.
			return nil, nil
		}
		randomUnit, err := secureRandomUnit()
		if err != nil {
			return nil, fmt.Errorf("select load-balanced surface target: %w", err)
		}
		pick := randomUnit * totalWeight
		start := 0
		for i, candidate := range candidates {
			pick -= candidate.weight
			if pick < 0 {
				start = i
				break
			}
		}
		rotated := make([]surfaceRankCandidate, 0, len(candidates))
		rotated = append(rotated, candidates[start:]...)
		rotated = append(rotated, candidates[:start]...)
		candidates = rotated
	case config.ModeLatency:
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
		case config.UnpricedStrategySkip:
			if len(priced) == 0 {
				// Wrapped, because "skip everything unpriced and nothing priced is
				// left" is the same answer as "no configured target serves this
				// model": the caller asked for something this gateway will not
				// route, and retrying cannot change it. Unwrapped it fell to the
				// classifier's catch-all and answered 500 — which every OpenAI SDK
				// retries — while the identical chat config answered 404.
				return nil, fmt.Errorf("no priced provider supports model %s: %w", model, core.ErrNoCapableProvider)
			}
			candidates = priced
		case config.UnpricedStrategyAllow:
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

// anyWeightExpressed reports whether the config states a weight at all.
//
// It is the discriminator Target.Weight cannot supply on its own: the field is
// a plain float64, so a target that never mentions weight is indistinguishable
// from one that sets it to 0. Asked across the whole target list the ambiguity
// resolves — a config that drains one target necessarily names a weight on
// another, since ValidateConfig rejects a loadbalance config whose weights sum
// to zero. So a weightless config means equal share, and inside a config that
// does use weights, 0 means 0.
func anyWeightExpressed(targets []config.Target) bool {
	for _, t := range targets {
		if t.Weight > 0 {
			return true
		}
	}
	return false
}

// activeSurfaceCandidates drops the candidates an operator has drained and
// normalises each survivor's weight into the share it takes, returning the
// survivors and their total.
//
// A target weighted 0 is DRAINED: it takes no traffic. Mapping 0 to 1 — which
// is what this did — kept sending live requests to a target at exactly the
// moment the operator had declared it out of service, which is the last thing
// wanted since draining before revoking a key is the documented way to retire
// one. Measured before this: 3 of 200 embedding requests still reached a target
// weighted 0.
func activeSurfaceCandidates(candidates []surfaceRankCandidate, weighted bool) ([]surfaceRankCandidate, float64) {
	if !weighted {
		for i := range candidates {
			candidates[i].weight = 1
		}
		return candidates, float64(len(candidates))
	}
	active := candidates[:0]
	total := 0.0
	for _, candidate := range candidates {
		if candidate.weight <= 0 {
			continue
		}
		active = append(active, candidate)
		total += candidate.weight
	}
	return active, total
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
		_, ok := providers.As[providers.EmbeddingProvider](p)
		return ok
	case surfaceImages:
		_, ok := providers.As[providers.ImageProvider](p)
		return ok
	case surfaceRerank:
		_, ok := providers.As[providers.RerankProvider](p)
		return ok
	case surfaceModeration:
		_, ok := providers.As[providers.ModerationProvider](p)
		return ok
	case surfaceTranscription:
		_, ok := providers.As[providers.TranscriptionProvider](p)
		return ok
	case surfaceSpeech:
		_, ok := providers.As[providers.SpeechProvider](p)
		return ok
	default:
		return false
	}
}
