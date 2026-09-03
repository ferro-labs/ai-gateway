package strategies

import (
	"fmt"
	"math/rand"
	"sort"

	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// CostOptimized routes to the cheapest compatible provider based on the model
// catalog's price for the request (see rankingUsage). By default, unpriced candidates are used
// only when no compatible provider has known pricing.
type CostOptimized struct {
	targets          []Target
	lookup           ProviderLookup
	catalog          models.Catalog
	unpricedStrategy unpricedStrategy
}

type unpricedStrategy string

const (
	unpricedStrategyFallback unpricedStrategy = "fallback"
	unpricedStrategySkip     unpricedStrategy = "skip"
	unpricedStrategyAllow    unpricedStrategy = "allow"
)

// NewCostOptimized creates a new cost-optimized strategy.
func NewCostOptimized(targets []Target, lookup ProviderLookup, catalog models.Catalog, unpricedStrategyConfig ...string) *CostOptimized {
	strategy := newUnpricedStrategy(unpricedStrategyConfig...)
	return &CostOptimized{targets: targets, lookup: lookup, catalog: catalog, unpricedStrategy: strategy}
}

// defaultRankingOutputTokens is the completion estimate when a request states
// no ceiling of its own.
const defaultRankingOutputTokens = 256

// rankingUsage is the usage every candidate is priced against for ordering.
//
// The prompt is estimated at ~4 characters per token and the completion at
// the request's own ceiling (max_tokens / max_completion_tokens) or a fixed
// default, so a target that is cheap to read and expensive to write is priced
// on both. Every other billable quantity is one unit: models.Calculate reads
// only the fields its catalog mode bills off, so one Usage prices a chat
// model on input plus output, an embedding model per token, an image model
// per image and an audio model per minute or character, and the same request
// orders the same targets on every surface. It is a routing heuristic only,
// not billing-accurate accounting.
func rankingUsage(req providers.Request) models.Usage {
	promptChars := 0
	for _, msg := range req.Messages {
		promptChars += len(msg.Content)
	}
	outputTokens := defaultRankingOutputTokens
	if ceiling, ok := req.EffectiveMaxTokens(); ok && ceiling > 0 {
		outputTokens = ceiling
	}
	return models.Usage{
		PromptTokens:     promptChars/4 + 1,
		CompletionTokens: outputTokens,
		ImageCount:       1,
		AudioInputSecs:   60,
		AudioOutputChars: 1000,
	}
}

// costOrderCandidate holds a candidate target with its estimated cost and
// catalog-pricing flags.
type costOrderCandidate struct {
	target     Target
	key        string
	costUSD    float64
	hasPrice   bool
	modelFound bool
}

// SelectTargets orders model-compatible targets by estimated cost, cheapest
// first; a run of equal-cost candidates leads with one drawn by weight
// (equally when no weight is set), since declaration order is not a contract. The unpriced strategy controls which cataloged-but-unpriced
// candidates rank: allow ranks any model-found candidate, skip and fallback rank
// only priced ones. Remaining targets follow in declared order; this mode does
// not advance past a failing target, so they stand in only when a preferred one
// is skipped (see Strategy.SelectTargets). In skip mode with no priced
// candidate it returns an error.
//
// When nothing ranks but some target does serve the model, the compatible
// candidates lead in declared order — the first COMPATIBLE target rather than
// targets[0].
func (c *CostOptimized) SelectTargets(req providers.Request) ([]string, error) {
	usage := rankingUsage(req)
	candidates := make([]costOrderCandidate, 0, len(c.targets))
	for _, t := range c.targets {
		p, ok := c.lookup(t.VirtualKey)
		if !ok || !p.SupportsModel(req.Model) {
			continue
		}
		// Price against the provider's canonical vendor identity, not the
		// (possibly aliased) routing key: an alias registered through
		// RegisterProviderAs shares its provider's catalog price rather than
		// pricing against a name the catalog has never heard of.
		model := req.Model
		if mapped := t.ModelMap[req.Model]; mapped != "" {
			model = mapped
		}
		result := models.Calculate(c.catalog, providers.CanonicalName(p)+"/"+model, usage)
		candidates = append(candidates, costOrderCandidate{
			target:     t,
			key:        t.VirtualKey,
			costUSD:    result.TotalUSD,
			hasPrice:   result.Priced,
			modelFound: result.ModelFound,
		})
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	ranked := make([]costOrderCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if !candidate.modelFound {
			continue
		}
		if !c.unpricedStrategy.ranksUnpricedCandidates() && !candidate.hasPrice {
			continue
		}
		ranked = append(ranked, candidate)
	}

	if len(ranked) == 0 {
		if c.unpricedStrategy.requiresPricedCandidate() {
			return nil, fmt.Errorf("no priced provider supports model %s: %w", req.Model, core.ErrNoCapableProvider)
		}
		keys := make([]string, 0, len(c.targets))
		for _, candidate := range candidates {
			keys = appendUniqueKey(keys, candidate.key)
		}
		return appendRemainingTargetKeys(keys, c.targets), nil
	}

	sort.SliceStable(ranked, func(i, j int) bool {
		return ranked[i].costUSD < ranked[j].costUSD
	})
	breakTiesByWeight(ranked)

	keys := make([]string, 0, len(c.targets))
	for _, candidate := range ranked {
		keys = appendUniqueKey(keys, candidate.key)
	}
	for _, candidate := range candidates {
		keys = appendUniqueKey(keys, candidate.key)
	}
	return appendRemainingTargetKeys(keys, c.targets), nil
}

func newUnpricedStrategy(config ...string) unpricedStrategy {
	if len(config) == 0 {
		return unpricedStrategyFallback
	}
	return parseUnpricedStrategy(config[0])
}

func parseUnpricedStrategy(strategy string) unpricedStrategy {
	switch unpricedStrategy(strategy) {
	case unpricedStrategySkip, unpricedStrategyAllow:
		return unpricedStrategy(strategy)
	default:
		return unpricedStrategyFallback
	}
}

func (s unpricedStrategy) ranksUnpricedCandidates() bool {
	return s == unpricedStrategyAllow
}

func (s unpricedStrategy) requiresPricedCandidate() bool {
	return s == unpricedStrategySkip
}

// breakTiesByWeight rotates every run of equal-cost candidates in ranked (which
// must already be sorted by cost) to start at a weight-drawn member. With no
// positive weight in the run the draw is uniform. A zero weight among weighted
// siblings never leads, as under loadbalance.
func breakTiesByWeight(ranked []costOrderCandidate) {
	for start := 0; start < len(ranked); {
		end := start + 1
		for end < len(ranked) && ranked[end].costUSD == ranked[start].costUSD {
			end++
		}
		if run := ranked[start:end]; len(run) > 1 {
			targets := make([]Target, len(run))
			for i, c := range run {
				targets[i] = c.target
			}
			lead, ok := weightedStartIndex(targets)
			if !ok {
				lead = rand.Intn(len(run)) //nolint:gosec // G404: tie-break draw, not security-sensitive
			}
			rotated := append(append(make([]costOrderCandidate, 0, len(run)), run[lead:]...), run[:lead]...)
			copy(run, rotated)
		}
		start = end
	}
}
