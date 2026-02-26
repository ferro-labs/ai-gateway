// Package strategies implements the routing strategies used by the gateway.
//
// Available strategies:
//   - Single:      always routes to one configured target.
//   - Fallback:    tries targets in order, retrying on failure.
//   - LoadBalance: distributes requests across targets by weight.
//   - Conditional: routes based on request field matching rules.
package strategies

import (
	"context"

	"github.com/ferro-labs/ai-gateway/providers"
)

// Strategy defines the interface for routing strategies.
type Strategy interface {
	// Execute runs the strategy and returns a response.
	Execute(ctx context.Context, req providers.Request) (*providers.Response, error)
}

// ProviderLookup resolves a provider name to a Provider instance.
type ProviderLookup func(name string) (providers.Provider, bool)
