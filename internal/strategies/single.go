package strategies

import (
	"github.com/ferro-labs/ai-gateway/providers"
)

// Target mirrors the gateway config target for use in strategies.
type Target struct {
	VirtualKey string
	Weight     float64
}

// Single routes all requests to a single provider.
type Single struct {
	// keys is the SelectTargets answer, built once. The order is a property of
	// the config, not of the request, so recomputing it per request would put
	// an allocation on the hot path for a slice that can never differ.
	// SelectTargets' contract is read-only, so sharing it is safe.
	keys []string
}

// NewSingle creates a new single-provider strategy.
func NewSingle(target Target) *Single {
	return &Single{keys: []string{target.VirtualKey}}
}

// SelectTargets returns the single configured target key. The returned slice is
// shared and must not be modified.
func (s *Single) SelectTargets(_ providers.Request) ([]string, error) {
	return s.keys, nil
}
