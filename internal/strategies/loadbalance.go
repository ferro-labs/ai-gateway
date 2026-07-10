package strategies

import (
	"context"
	"fmt"

	"github.com/ferro-labs/ai-gateway/providers"
)

// LoadBalance distributes requests across targets using weighted random selection.
type LoadBalance struct {
	targets []Target
	lookup  ProviderLookup
}

// NewLoadBalance creates a new load balance strategy.
func NewLoadBalance(targets []Target, lookup ProviderLookup) *LoadBalance {
	return &LoadBalance{
		targets: targets,
		lookup:  lookup,
	}
}

// Execute selects a provider by weighted random selection and sends the request.
// Only targets whose provider supports the requested model are considered.
func (lb *LoadBalance) Execute(ctx context.Context, req providers.Request) (*providers.Response, error) {
	if len(lb.targets) == 0 {
		return nil, fmt.Errorf("no targets configured for loadbalance")
	}

	// Filter to targets that support the requested model.
	var compatible []Target
	for _, t := range lb.targets {
		p, ok := lb.lookup(t.VirtualKey)
		if ok && p.SupportsModel(req.Model) {
			compatible = append(compatible, t)
		}
	}
	if len(compatible) == 0 {
		return nil, fmt.Errorf("no provider supports model %s", req.Model)
	}

	target, err := lb.selectFromTargets(compatible)
	if err != nil {
		return nil, err
	}

	return dispatch(ctx, lb.lookup, target, req, "load balancing based routing: provider not found")
}

// selectFromTargets picks a target from the given slice using weighted random
// selection. weightedPick draws from the top-level math/rand source, which is
// safe for concurrent use, so no additional locking is required here.
func (lb *LoadBalance) selectFromTargets(targets []Target) (Target, error) {
	t, ok := weightedPick(targets, func(t Target) float64 {
		return effectiveWeight(t.Weight)
	})
	if !ok {
		return Target{}, fmt.Errorf("no targets available")
	}
	return t, nil
}

// SelectTargets returns all targets rotated from a weight-biased start index,
// so the first attempted target is chosen by weight while the remainder stay
// available as fallbacks.
func (lb *LoadBalance) SelectTargets(_ providers.Request) ([]string, error) {
	if len(lb.targets) == 0 {
		return nil, nil
	}
	startIdx := weightedStartIndex(lb.targets)
	keys := make([]string, 0, len(lb.targets))
	for i := 0; i < len(lb.targets); i++ {
		keys = append(keys, lb.targets[(startIdx+i)%len(lb.targets)].VirtualKey)
	}
	return keys, nil
}
