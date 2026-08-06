package strategies

import (
	"fmt"
	"sort"

	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// CostOptimized routes to the cheapest compatible provider based on estimated
// input cost from the model catalog. By default, unpriced candidates are used
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

// estimatePromptTokens approximates the prompt token count at ~4 characters per
// token. It is a routing heuristic only, not billing-accurate accounting.
func estimatePromptTokens(req providers.Request) int {
	promptChars := 0
	for _, msg := range req.Messages {
		promptChars += len(msg.Content)
	}
	return promptChars/4 + 1
}

// costOrderCandidate holds a candidate target with its estimated input cost and
// catalog-pricing flags.
type costOrderCandidate struct {
	key        string
	costUSD    float64
	hasPrice   bool
	modelFound bool
}

// SelectTargets orders model-compatible targets by estimated input cost,
// cheapest first. The unpriced strategy controls which cataloged-but-unpriced
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
	estimatedPromptTokens := estimatePromptTokens(req)
	candidates := make([]costOrderCandidate, 0, len(c.targets))
	for _, t := range c.targets {
		if !routableCandidate(c.lookup, t.VirtualKey, req.Model) {
			continue
		}
		result := models.Calculate(c.catalog, t.VirtualKey+"/"+req.Model, models.Usage{
			PromptTokens: estimatedPromptTokens,
		})
		candidates = append(candidates, costOrderCandidate{
			key:        t.VirtualKey,
			costUSD:    result.InputUSD,
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
