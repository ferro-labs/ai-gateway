package strategies

import (
	"context"
	"fmt"

	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/providers"
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

type priced struct {
	target   Target
	costUSD  float64
	hasPrice bool
	isModel  bool
}

// NewCostOptimized creates a new cost-optimized strategy.
func NewCostOptimized(targets []Target, lookup ProviderLookup, catalog models.Catalog, unpricedStrategyConfig ...string) *CostOptimized {
	strategy := newUnpricedStrategy(unpricedStrategyConfig...)
	return &CostOptimized{targets: targets, lookup: lookup, catalog: catalog, unpricedStrategy: strategy}
}

// Execute selects the provider with the lowest estimated input cost for the
// request and forwards it there. Prompt token count is estimated at roughly
// 4 characters per token.
func (c *CostOptimized) Execute(ctx context.Context, req providers.Request) (*providers.Response, error) {
	if len(c.targets) == 0 {
		return nil, fmt.Errorf("no targets configured for cost-optimized strategy")
	}

	// Estimate prompt tokens: ~4 chars per token (rough heuristic for routing).
	promptChars := 0
	for _, msg := range req.Messages {
		promptChars += len(msg.Content)
	}
	estimatedPromptTokens := promptChars/4 + 1
	var candidates []priced
	for _, t := range c.targets {
		p, ok := c.lookup(t.VirtualKey)
		if !ok || !p.SupportsModel(req.Model) {
			continue
		}
		result := models.Calculate(c.catalog, t.VirtualKey+"/"+req.Model, models.Usage{
			PromptTokens: estimatedPromptTokens,
		})
		candidates = append(candidates, priced{
			target:   t,
			costUSD:  result.InputUSD,
			hasPrice: result.Priced,
			isModel:  result.ModelFound,
		})
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("no provider supports model %s", req.Model)
	}

	// Unpriced candidates are providers that support the model but do not have
	// usable input-token pricing in the catalog. The mode controls whether those
	// candidates are excluded, used only when nothing is priced, or treated as
	// normal zero-cost candidates.
	best, err := selectCostOptimizedCandidate(candidates, c.unpricedStrategy, req.Model)
	if err != nil {
		return nil, err
	}

	return dispatch(ctx, c.lookup, best.target, req, "cost optimized routing: provider not found")
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

func selectCostOptimizedCandidate(candidates []priced, strategy unpricedStrategy, model string) (*priced, error) {
	var best *priced
	for i := range candidates {
		candidate := &candidates[i]
		if !candidate.isModel {
			continue
		}
		if !strategy.ranksUnpricedCandidates() && !candidate.hasPrice {
			continue
		}
		if best == nil || candidate.costUSD < best.costUSD {
			best = candidate
		}
	}

	if best != nil {
		return best, nil
	} else if strategy.requiresPricedCandidate() {
		// No cataloged/priced candidate is selectable; return an error.
		return nil, fmt.Errorf("no priced provider supports model %s", model)
	}
	// Preserve historical fallback behavior: when no cataloged/priced candidate
	// is selectable, fallback and allow route to the first compatible target.
	return &candidates[0], nil
}
