package aigateway

import (
	"fmt"
	"maps"
	"regexp"
	"strings"

	"github.com/ferro-labs/ai-gateway/internal/strategies"
	"github.com/ferro-labs/ai-gateway/providers"
)

// Strategy construction from Gateway config plus the content-condition helpers
// used by conditional / content-based / A-B-test routing.

type streamingContentCondition struct {
	ContentCondition
	re *regexp.Regexp
}

// getStrategy lazily builds the strategy from config and registered providers.
// Circuit breakers are built once and applied in the provider lookup closure.
func (g *Gateway) getStrategy() (strategies.Strategy, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.strategy != nil {
		return g.strategy, nil
	}

	g.ensureCircuitBreakersLocked()

	// Snapshot both maps under the write lock already held. The lookup closure
	// runs inside Strategy.Execute with no lock held, so capturing local copies
	// here is the only safe access pattern.
	// maps.Clone is a shallow copy — safe because map values (Provider, *CB) are
	// themselves immutable references; we never mutate through them in the closure.
	providerSnap := maps.Clone(g.providers)
	cbSnap := maps.Clone(g.circuitBreakers)

	// Provider lookup with transparent circuit-breaker wrapping.
	//
	// The closure is captured into the strategy and invoked later from the
	// request hot path, AFTER Route/RouteStream have released g.mu. It reads
	// from the snapshots captured above, so no lock is needed in the closure.
	lookup := func(name string) (providers.Provider, bool) {
		p, ok := providerSnap[name]
		if !ok {
			return nil, false
		}
		if cb, hasCB := cbSnap[name]; hasCB {
			return &cbProvider{Provider: p, cb: cb, name: name}, true
		}
		return p, ok
	}

	targets := make([]strategies.Target, len(g.config.Targets))
	for i, t := range g.config.Targets {
		targets[i] = strategies.Target{
			VirtualKey: t.VirtualKey,
			Weight:     t.Weight,
		}
	}

	var s strategies.Strategy
	switch g.config.Strategy.Mode {
	case ModeSingle, "":
		if len(targets) == 0 {
			return nil, fmt.Errorf("no targets configured for single strategy")
		}
		s = strategies.NewSingle(targets[0], lookup)
	case ModeFallback:
		fb := strategies.NewFallback(targets, lookup)
		for _, t := range g.config.Targets {
			if t.Retry == nil {
				continue
			}
			fb.WithTargetRetry(t.VirtualKey, t.Retry.Attempts, t.Retry.OnStatusCodes, t.Retry.InitialBackoffMs)
		}
		s = fb
	case ModeLoadBalance:
		s = strategies.NewLoadBalance(targets, lookup)
	case ModeLatency:
		if len(targets) == 0 {
			return nil, fmt.Errorf("no targets configured for least-latency strategy")
		}
		s = strategies.NewLeastLatency(targets, lookup, g.latencyTracker)
	case ModeCostOptimized:
		if len(targets) == 0 {
			return nil, fmt.Errorf("no targets configured for cost-optimized strategy")
		}
		s = strategies.NewCostOptimized(targets, lookup, g.catalog, g.config.Strategy.UnpricedStrategy)
	case ModeConditional:
		if len(g.config.Strategy.Conditions) == 0 {
			return nil, fmt.Errorf("no conditions configured for conditional strategy")
		}
		if len(targets) == 0 {
			return nil, fmt.Errorf("no targets configured for conditional strategy")
		}
		var rules []strategies.ConditionRule
		for _, cond := range g.config.Strategy.Conditions {
			rules = append(rules, strategies.ConditionRule{
				Key:    cond.Key,
				Value:  cond.Value,
				Target: strategies.Target{VirtualKey: cond.TargetKey},
			})
		}
		s = strategies.NewConditional(rules, targets[0], lookup)
	case ModeContentBased:
		cbs, err := g.buildContentBasedStrategy(targets, lookup)
		if err != nil {
			return nil, err
		}
		s = cbs
	case ModeABTest:
		abt, err := g.buildABTestStrategy(lookup)
		if err != nil {
			return nil, err
		}
		s = abt
	default:
		return nil, fmt.Errorf("unknown strategy mode: %s", g.config.Strategy.Mode)
	}

	g.strategy = s
	return s, nil
}

// buildContentBasedStrategy constructs a ContentBased strategy from the gateway config.
func (g *Gateway) buildContentBasedStrategy(targets []strategies.Target, lookup strategies.ProviderLookup) (strategies.Strategy, error) {
	if len(g.config.Strategy.ContentConditions) == 0 {
		return nil, fmt.Errorf("no content_conditions configured for content-based strategy")
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("no targets configured for content-based strategy")
	}
	var rules []strategies.ContentRule
	for _, cc := range g.config.Strategy.ContentConditions {
		rules = append(rules, strategies.ContentRule{
			Type:   strategies.ContentConditionType(cc.Type),
			Value:  cc.Value,
			Target: strategies.Target{VirtualKey: cc.TargetKey},
		})
	}
	return strategies.NewContentBased(rules, targets[0], lookup)
}

// buildABTestStrategy constructs an ABTest strategy from the gateway config.
func (g *Gateway) buildABTestStrategy(lookup strategies.ProviderLookup) (strategies.Strategy, error) {
	if len(g.config.Strategy.ABVariants) == 0 {
		return nil, fmt.Errorf("no ab_variants configured for ab-test strategy")
	}
	var variants []strategies.ABTestVariant
	for _, v := range g.config.Strategy.ABVariants {
		variants = append(variants, strategies.ABTestVariant{
			Target: strategies.Target{VirtualKey: v.TargetKey},
			Weight: v.Weight,
			Label:  v.Label,
		})
	}
	return strategies.NewABTest(variants, lookup)
}

func conditionMatches(cond Condition, model string) bool {
	switch cond.Key {
	case "model":
		return model == cond.Value
	case "model_prefix":
		return strings.HasPrefix(model, cond.Value)
	default:
		return false
	}
}

func compileStreamingContentConditions(mode StrategyMode, conditions []ContentCondition) ([]streamingContentCondition, error) {
	if mode != ModeContentBased {
		return nil, nil
	}
	compiled := make([]streamingContentCondition, len(conditions))
	for i, cond := range conditions {
		compiled[i].ContentCondition = cond
		if cond.Type != "prompt_regex" {
			continue
		}
		re, err := regexp.Compile(cond.Value)
		if err != nil {
			return nil, fmt.Errorf("streaming content-based routing: invalid regex %q in rule %d: %w", cond.Value, i, err)
		}
		compiled[i].re = re
	}
	return compiled, nil
}

// streamingContentConditionMatches evaluates a single ContentCondition against
// a request, mirroring the logic in internal/strategies/contentbased.go.
func streamingContentConditionMatches(cond streamingContentCondition, req providers.Request) bool {
	switch cond.Type {
	case "prompt_contains":
		lower := strings.ToLower(cond.Value)
		for _, msg := range req.Messages {
			if msg.Role == roleUser && strings.Contains(strings.ToLower(msg.Content), lower) {
				return true
			}
		}
		return false
	case "prompt_not_contains":
		lower := strings.ToLower(cond.Value)
		for _, msg := range req.Messages {
			if msg.Role == roleUser && strings.Contains(strings.ToLower(msg.Content), lower) {
				return false
			}
		}
		return true
	case "prompt_regex":
		if cond.re == nil {
			return false
		}
		for _, msg := range req.Messages {
			if msg.Role == roleUser && cond.re.MatchString(msg.Content) {
				return true
			}
		}
		return false
	default:
		return false
	}
}
