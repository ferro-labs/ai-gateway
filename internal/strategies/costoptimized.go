package strategies

import (
	"context"
	"fmt"

	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/providers"
)

// CostOptimized routes to the cheapest compatible provider based on estimated
// input cost from the model catalog. When catalog pricing is unavailable for
// all candidates, the first compatible provider is used as a fallback.
type CostOptimized struct {
	targets []Target
	lookup  ProviderLookup
	catalog models.Catalog
}

// NewCostOptimized creates a new cost-optimized strategy.
func NewCostOptimized(targets []Target, lookup ProviderLookup, catalog models.Catalog) *CostOptimized {
	return &CostOptimized{targets: targets, lookup: lookup, catalog: catalog}
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

	type priced struct {
		target   Target
		costUSD  float64
		hasPrice bool
	}

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
			hasPrice: result.ModelFound,
		})
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("no provider supports model %s", req.Model)
	}

	// Among providers with catalog pricing, pick the cheapest.
	var best *priced
	for i := range candidates {
		entry := &candidates[i]
		if !entry.hasPrice {
			continue
		}
		if best == nil || entry.costUSD < best.costUSD {
			best = entry
		}
	}

	// No pricing found — fall back to the first compatible provider.
	if best == nil {
		best = &candidates[0]
	}

	p, _ := c.lookup(best.target.VirtualKey)
	return p.Complete(ctx, req)
}
