package strategies

import (
	"context"
	"fmt"

	"github.com/ferro-labs/ai-gateway/providers"
)

// Target mirrors the gateway config target for use in strategies.
type Target struct {
	VirtualKey string
	Weight     float64
}

// Single routes all requests to a single provider.
type Single struct {
	target Target
	lookup ProviderLookup
}

// NewSingle creates a new single-provider strategy.
func NewSingle(target Target, lookup ProviderLookup) *Single {
	return &Single{target: target, lookup: lookup}
}

// Execute sends the request to the single configured provider.
func (s *Single) Execute(ctx context.Context, req providers.Request) (*providers.Response, error) {
	p, ok := s.lookup(s.target.VirtualKey)
	if !ok {
		return nil, fmt.Errorf("provider not found: %s", s.target.VirtualKey)
	}
	if !p.SupportsModel(req.Model) {
		return nil, fmt.Errorf("provider %s does not support model %s", s.target.VirtualKey, req.Model)
	}
	return p.Complete(ctx, req)
}
