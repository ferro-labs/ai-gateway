package aigateway

import (
	"fmt"
	"maps"
	"time"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/internal/latency"
	"github.com/ferro-labs/ai-gateway/internal/strategies"
	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/providers"
)

// Strategy construction from Gateway config.

// getStrategy lazily builds the chat strategy from config and registered
// providers. Circuit breakers are built once and applied in the provider
// lookup closure.
func (g *Gateway) getStrategy() (strategies.Strategy, error) {
	return g.strategyFor("")
}

// strategyFor is getStrategy for one surface. It is the same strategy built
// from the same config; only the provider lookup differs, and it differs by
// exactly one rule: a target whose provider cannot serve this surface is not a
// candidate. That keeps a chat-only target from winning a weighted draw it can
// never serve — which would then hand its whole share to whichever capable
// target follows it in the rotation — or from taking the cheapest or fastest
// slot under a ranking mode. Model eligibility, order, and the tail are then
// decided once, in internal/strategies, for chat and every other surface.
func (g *Gateway) strategyFor(surface string) (strategies.Strategy, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if s, ok := g.strategies[surface]; ok {
		return s, nil
	}

	g.ensureCircuitBreakersLocked()
	g.ensureProviderLimitersLocked()

	// Snapshot every map the lookup closure reads, under the write lock already
	// held. The closure runs inside Strategy.SelectTargets with no lock held, so
	// capturing local copies here is the only safe access pattern.
	// maps.Clone is a shallow copy — safe because map values (Provider, *CB) are
	// themselves immutable references; we never mutate through them in the closure.
	cbSnap := maps.Clone(g.circuitBreakers)
	limSnap := maps.Clone(g.limiters)

	// The provider snapshot holds each provider under the routing-index view of
	// its model set: every strategy selects a target on SupportsModel, and that
	// answer must cover the same models FindByModel admits and /v1/models
	// advertises — a provider's own SupportsModel is often much narrower.
	// Building the views here rather than per lookup keeps the request path
	// allocation-free, and the snapshot is rebuilt whenever the index is,
	// because both are replaced under the same lock that clears g.strategies.
	providerSnap := make(map[string]providers.Provider, len(g.providers))
	for name, p := range g.providers {
		if surface != "" && !providerSupportsSurface(p, surface) {
			continue
		}
		providerSnap[name] = withIndexedModels(name, p, g.modelIndex.exactProviders)
	}

	// Provider lookup with transparent circuit-breaker and concurrency-limit
	// decoration.
	//
	// The closure is captured into the strategy and invoked later from the
	// request hot path, AFTER Route/RouteStream have released g.mu. It reads
	// from the snapshots captured above, so no lock is needed in the closure.
	lookup := func(name string) (providers.Provider, bool) {
		p, ok := providerSnap[name]
		if !ok {
			return nil, false
		}
		return decorateProvider(name, p, cbSnap[name], limSnap[name]), true
	}

	targets := make([]strategies.Target, len(g.config.Targets))
	for i, t := range g.config.Targets {
		targets[i] = strategies.Target{
			VirtualKey: t.VirtualKey,
			Weight:     t.Weight,
			ModelMap:   t.ModelMap,
		}
	}

	s, err := buildStrategy(g.config.Strategy, targets, lookup, g.latencyTracker, g.catalog)
	if err != nil {
		return nil, err
	}
	if g.strategies == nil {
		g.strategies = make(map[string]strategies.Strategy)
	}
	g.strategies[surface] = s
	return s, nil
}

// buildStrategy maps a validated StrategyConfig onto a strategy.
//
// Every error it can return is one config.ValidateConfig has already rejected,
// which is the point: strategies are built LAZILY, on the first request that
// needs one, so an error here would be a 500 on a request no provider was ever
// asked to serve. Construction stays lazy because it must — the provider lookup
// snapshots the registry, and providers are registered after New returns — so
// what makes that safe is validation being total over these failures rather than
// construction moving earlier. TestValidateConfigCoversStrategyConstruction
// holds the two in step.
func buildStrategy(
	cfg config.StrategyConfig,
	targets []strategies.Target,
	lookup strategies.ProviderLookup,
	tracker *latency.Tracker,
	catalog models.Catalog,
) (strategies.Strategy, error) {
	if len(targets) == 0 {
		return nil, fmt.Errorf("no targets configured for %s strategy", cfg.Mode)
	}
	switch cfg.Mode {
	case config.ModeSingle, "":
		return strategies.NewSingle(targets[0]), nil
	case config.ModeFallback:
		return strategies.NewFallback(targets), nil
	case config.ModeLoadBalance:
		return strategies.NewLoadBalance(targets, lookup).WithSticky(stickyFrom(cfg.Sticky)), nil
	case config.ModeLatency:
		return strategies.NewLeastLatency(targets, lookup, tracker), nil
	case config.ModeCostOptimized:
		return strategies.NewCostOptimized(targets, lookup, catalog, cfg.UnpricedStrategy), nil
	case config.ModeConditional:
		rules := make([]strategies.ConditionRule, 0, len(cfg.Conditions))
		for _, cond := range cfg.Conditions {
			rules = append(rules, strategies.ConditionRule{
				Key:    cond.Key,
				Value:  cond.Value,
				Target: strategies.Target{VirtualKey: cond.TargetKey},
			})
		}
		return strategies.NewConditional(rules, targets[0]).WithRoutingTargets(targets), nil
	case config.ModeContentBased:
		rules := make([]strategies.ContentRule, 0, len(cfg.ContentConditions))
		for _, cc := range cfg.ContentConditions {
			rules = append(rules, strategies.ContentRule{
				Type:   strategies.ContentConditionType(cc.Type),
				Value:  cc.Value,
				Target: strategies.Target{VirtualKey: cc.TargetKey},
			})
		}
		cb, err := strategies.NewContentBased(rules, targets[0])
		if err != nil {
			return nil, err
		}
		return cb.WithRoutingTargets(targets), nil
	case config.ModeABTest:
		variants := make([]strategies.ABTestVariant, 0, len(cfg.ABVariants))
		for _, v := range cfg.ABVariants {
			variants = append(variants, strategies.ABTestVariant{
				Target: strategies.Target{VirtualKey: v.TargetKey},
				Weight: v.Weight,
				Label:  v.Label,
			})
		}
		abt, err := strategies.NewABTest(variants, lookup)
		if err != nil {
			return nil, err
		}
		return abt.WithRoutingTargets(targets).WithSticky(stickyFrom(cfg.Sticky)), nil
	default:
		return nil, fmt.Errorf("unknown strategy mode: %s", cfg.Mode)
	}
}

// stickyFrom maps strategy.sticky onto the strategies' value. The TTL was
// validated at load, so a parse failure here reads as no TTL.
func stickyFrom(cfg *config.StickyConfig) strategies.Sticky {
	if cfg == nil {
		return strategies.Sticky{}
	}
	ttl, _ := time.ParseDuration(cfg.TTL)
	return strategies.Sticky{On: cfg.On, TTL: ttl}
}
