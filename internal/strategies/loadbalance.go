package strategies

import (
	"context"
	"fmt"
	"sync"

	"github.com/ferro-labs/ai-gateway/providers"
)

// LoadBalance distributes requests across targets using weighted random selection.
type LoadBalance struct {
	targets []Target
	lookup  ProviderLookup
	mu      sync.Mutex
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

// selectFromTargets picks a target from the given slice using weighted random selection.
func (lb *LoadBalance) selectFromTargets(targets []Target) (Target, error) {
	lb.mu.Lock()
	defer lb.mu.Unlock()

	t, ok := weightedPick(targets, func(t Target) float64 {
		return effectiveWeight(t.Weight)
	})
	if !ok {
		return Target{}, fmt.Errorf("no targets available")
	}
	return t, nil
}
