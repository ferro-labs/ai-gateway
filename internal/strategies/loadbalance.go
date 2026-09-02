package strategies

import (
	"github.com/ferro-labs/ai-gateway/providers"
)

// LoadBalance distributes requests across targets using weighted random selection.
type LoadBalance struct {
	targets []Target
	lookup  ProviderLookup
	sticky  Sticky
}

// WithSticky pins each request's start target to a hash of its sticky key
// (see Sticky). Returns the receiver so callers can chain it.
func (lb *LoadBalance) WithSticky(s Sticky) *LoadBalance {
	lb.sticky = s
	return lb
}

// NewLoadBalance creates a new load balance strategy.
func NewLoadBalance(targets []Target, lookup ProviderLookup) *LoadBalance {
	return &LoadBalance{
		targets: targets,
		lookup:  lookup,
	}
}

// compatibleTargets returns the subset of lb.targets whose provider is
// registered and supports model, preserving declared order.
func (lb *LoadBalance) compatibleTargets(model string) []Target {
	var compatible []Target
	for _, t := range lb.targets {
		p, ok := lb.lookup(t.VirtualKey)
		if ok && p.SupportsModel(model) {
			compatible = append(compatible, t)
		}
	}
	return compatible
}

// SelectTargets returns the model-compatible targets rotated from a
// weight-biased start index, so the first attempted target is chosen by weight
// while the remainder stay available as fallbacks. Targets whose provider is
// missing or does not support the model are excluded.
//
// It returns nil when no compatible target carries a positive weight. That is
// not the same as "no compatible target": it means the only targets that could
// serve this model have been drained to zero, and answering it from a drained
// target would undo the drain at exactly the moment the operator is relying on
// it — the credential is about to be revoked. A drained target is as unavailable
// as a missing one, so the caller reports the same 404.
func (lb *LoadBalance) SelectTargets(req providers.Request) ([]string, error) {
	compatible := lb.compatibleTargets(req.Model)
	if len(compatible) == 0 {
		return nil, nil
	}
	startIdx, ok := weightedStartIndexAt(compatible, lb.sticky.unit(req.User))
	if !ok {
		return nil, nil
	}
	keys := make([]string, 0, len(compatible))
	for i := 0; i < len(compatible); i++ {
		keys = append(keys, compatible[(startIdx+i)%len(compatible)].VirtualKey)
	}
	return keys, nil
}
