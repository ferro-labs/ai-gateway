package strategies

import (
	"context"
	"fmt"
	"sort"

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
	// isAggregator reports whether the provider registered under the given
	// routing alias (virtual key) is a routing aggregator (e.g. OpenRouter,
	// NanoGPT). May be nil, in which case no candidate is treated as an
	// aggregator.
	isAggregator func(virtualKey string) bool
}

type unpricedStrategy string

const (
	unpricedStrategyFallback unpricedStrategy = "fallback"
	unpricedStrategySkip     unpricedStrategy = "skip"
	unpricedStrategyAllow    unpricedStrategy = "allow"
)

type priced struct {
	target       Target
	costUSD      float64
	hasPrice     bool
	isModel      bool
	isAggregator bool
}

// NewCostOptimized creates a new cost-optimized strategy.
func NewCostOptimized(targets []Target, lookup ProviderLookup, catalog models.Catalog, unpricedStrategyConfig ...string) *CostOptimized {
	strategy := newUnpricedStrategy(unpricedStrategyConfig...)
	return &CostOptimized{targets: targets, lookup: lookup, catalog: catalog, unpricedStrategy: strategy}
}

// WithAggregatorPredicate sets the predicate used to identify routing
// aggregators (providers whose catalog-listed price is not representative of
// actual request cost, e.g. OpenRouter, NanoGPT). Returns the receiver for
// chaining.
func (c *CostOptimized) WithAggregatorPredicate(fn func(virtualKey string) bool) *CostOptimized {
	c.isAggregator = fn
	return c
}

// Execute selects the provider with the lowest estimated input cost for the
// request and forwards it there. Prompt token count is estimated at roughly
// 4 characters per token.
func (c *CostOptimized) Execute(ctx context.Context, req providers.Request) (*providers.Response, error) {
	if len(c.targets) == 0 {
		return nil, fmt.Errorf("no targets configured for cost-optimized strategy")
	}

	estimatedPromptTokens := estimatePromptTokens(req)
	var candidates []priced
	for _, t := range c.targets {
		p, ok := c.lookup(t.VirtualKey)
		if !ok || !p.SupportsModel(req.Model) {
			continue
		}
		// Use the provider's real canonical type (p.Name()), not the routing
		// alias (t.VirtualKey), for the catalog lookup key. Multi-instance
		// targets (e.g. two Ollama Cloud accounts registered under distinct
		// aliases) share one catalog entry keyed by canonical type; looking up
		// by alias would silently miss pricing for every alias but the first.
		result := models.Calculate(c.catalog, p.Name()+"/"+req.Model, models.Usage{
			PromptTokens: estimatedPromptTokens,
		})
		candidates = append(candidates, priced{
			target:   t,
			costUSD:  result.InputUSD,
			hasPrice: result.Priced,
			isModel:  result.ModelFound,
			// A routing aggregator's catalog-listed price can't be trusted as
			// a ranking signal: aggregators (e.g. OpenRouter, NanoGPT) route
			// each request to whichever underlying provider/model they pick
			// at call time, so the static catalog entry doesn't necessarily
			// reflect what a given request will actually cost. This is not
			// about hidden markup — OpenRouter, for example, passes through
			// underlying token pricing with no per-token markup — it's that
			// the price is for an unknown, dynamically-selected upstream.
			isAggregator: c.isAggregator != nil && c.isAggregator(t.VirtualKey),
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

// estimatePromptTokens approximates the prompt token count at ~4 characters per
// token. It is a routing heuristic only, not billing-accurate accounting.
func estimatePromptTokens(req providers.Request) int {
	promptChars := 0
	for _, msg := range req.Messages {
		promptChars += len(msg.Content)
	}
	return promptChars/4 + 1
}

// costOrderCandidate holds a streaming-capable target with its estimated input
// cost and catalog-pricing flags.
type costOrderCandidate struct {
	key          string
	costUSD      float64
	hasPrice     bool
	modelFound   bool
	isAggregator bool
}

// SelectTargets orders streaming-capable targets by estimated input cost,
// cheapest first. The unpriced strategy controls which cataloged-but-unpriced
// candidates rank: allow ranks any model-found candidate, skip and fallback rank
// only priced ones. Remaining targets are appended as fallbacks. In skip mode
// with no priced candidate it returns an error, matching Execute.
func (c *CostOptimized) SelectTargets(req providers.Request) ([]string, error) {
	estimatedPromptTokens := estimatePromptTokens(req)
	candidates := make([]costOrderCandidate, 0, len(c.targets))
	for _, t := range c.targets {
		p, ok := c.lookup(t.VirtualKey)
		if !ok || !p.SupportsModel(req.Model) {
			continue
		}
		if _, isStream := p.(providers.StreamProvider); !isStream {
			continue
		}
		// Use the provider's real canonical type (p.Name()), not the routing
		// alias (t.VirtualKey), for the catalog lookup key — see the matching
		// comment in Execute above.
		result := models.Calculate(c.catalog, p.Name()+"/"+req.Model, models.Usage{
			PromptTokens: estimatedPromptTokens,
		})
		candidates = append(candidates, costOrderCandidate{
			key:        t.VirtualKey,
			costUSD:    result.InputUSD,
			hasPrice:   result.Priced,
			modelFound: result.ModelFound,
			// See the isAggregator comment in Execute: a routing aggregator's
			// catalog price reflects an opaque, dynamically-chosen upstream
			// call, so it can't be trusted as a ranking signal.
			isAggregator: c.isAggregator != nil && c.isAggregator(t.VirtualKey),
		})
	}
	if len(candidates) == 0 {
		return targetKeys(c.targets), nil
	}

	ranked := make([]costOrderCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if !candidate.modelFound {
			continue
		}
		// Aggregator candidates never rank on catalog price; they're only
		// used as trailing fallbacks (added back in below via
		// appendRemainingTargetKeys / the raw candidates loop), same as any
		// other excluded candidate.
		if candidate.isAggregator {
			continue
		}
		if !c.unpricedStrategy.ranksUnpricedCandidates() && !candidate.hasPrice {
			continue
		}
		ranked = append(ranked, candidate)
	}

	if len(ranked) == 0 {
		if c.unpricedStrategy.requiresPricedCandidate() {
			// Allow an aggregator to serve as a last-resort ordering when no
			// non-aggregator candidate is viable at all — but still order
			// non-aggregators first so one isn't preferred merely because of
			// its position in the config.
			if hasAggregatorCandidate(candidates) {
				return appendRemainingTargetKeys(nonAggregatorsFirst(candidates), c.targets), nil
			}
			return nil, fmt.Errorf("no priced provider supports model %s", req.Model)
		}
		return targetKeys(c.targets), nil
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

func selectCostOptimizedCandidate(candidates []priced, strategy unpricedStrategy, model string) (*priced, error) {
	var best *priced
	for i := range candidates {
		candidate := &candidates[i]
		if !candidate.isModel {
			continue
		}
		// Aggregator candidates never win on catalog price, regardless of
		// unpricedStrategy: their listed price does not represent what a
		// real request will cost, so it can't be trusted as a ranking
		// signal. They remain in the pool and are only used below as a
		// last-resort fallback when no non-aggregator candidate exists.
		if candidate.isAggregator {
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
		// Before giving up entirely, allow an aggregator to serve as a
		// last-resort fallback if it's literally the only candidate that
		// supports the model — better to route somewhere than nowhere.
		if agg := firstAggregatorCandidate(candidates); agg != nil {
			return agg, nil
		}
		// No cataloged/priced candidate is selectable; return an error.
		return nil, fmt.Errorf("no priced provider supports model %s", model)
	}
	// Preserve historical fallback behavior: when no cataloged/priced candidate
	// is selectable, fall back to the first compatible target — but prefer a
	// non-aggregator so an aggregator only wins here because it's genuinely
	// the only option, not merely because it happens to be listed first.
	for i := range candidates {
		if !candidates[i].isAggregator {
			return &candidates[i], nil
		}
	}
	return &candidates[0], nil
}

// firstAggregatorCandidate returns the first model-supporting aggregator
// candidate, or nil if none exists.
func firstAggregatorCandidate(candidates []priced) *priced {
	for i := range candidates {
		if candidates[i].isModel && candidates[i].isAggregator {
			return &candidates[i]
		}
	}
	return nil
}

// hasAggregatorCandidate reports whether any model-supporting candidate is a
// routing aggregator.
func hasAggregatorCandidate(candidates []costOrderCandidate) bool {
	for _, c := range candidates {
		if c.modelFound && c.isAggregator {
			return true
		}
	}
	return false
}

// nonAggregatorsFirst returns candidate keys ordered with non-aggregators
// first and aggregators last (each group preserving relative order),
// deduplicated — so an aggregator is only preferred when it's the only
// viable option, never merely because of its position in the config.
func nonAggregatorsFirst(candidates []costOrderCandidate) []string {
	keys := make([]string, 0, len(candidates))
	for _, c := range candidates {
		if !c.isAggregator {
			keys = appendUniqueKey(keys, c.key)
		}
	}
	for _, c := range candidates {
		if c.isAggregator {
			keys = appendUniqueKey(keys, c.key)
		}
	}
	return keys
}
