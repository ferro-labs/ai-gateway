// Package strategies implements the routing strategies used by the gateway.
//
// Available strategies:
//   - Single:        always routes to one configured target.
//   - Fallback:      tries targets in order, retrying on failure.
//   - LoadBalance:   distributes requests across targets by weight.
//   - Conditional:   routes based on request field matching rules.
//   - LeastLatency:  routes to the target with the lowest observed p50 latency.
//   - CostOptimized: routes to the cheapest catalog-priced target.
//   - ContentBased:  routes based on the textual content of prompt messages.
//   - ABTest:        weighted random traffic splitting across labelled variants.
package strategies

import (
	"context"

	"github.com/ferro-labs/ai-gateway/providers"
)

// Strategy defines the interface for routing strategies.
type Strategy interface {
	// Execute runs the strategy and returns a response.
	Execute(ctx context.Context, req providers.Request) (*providers.Response, error)

	// SelectTargets returns the ordered target virtual keys the strategy would
	// try for req, most-preferred first, with any remaining configured targets
	// appended as fallbacks. It is the streaming counterpart to Execute's
	// implicit provider selection so Route and RouteStream share one ordering.
	// Only CostOptimized returns an error: in skip mode when no priced provider
	// supports the model. All other strategies return a nil error.
	SelectTargets(req providers.Request) ([]string, error)
}

// ProviderLookup resolves a provider name to a Provider instance.
type ProviderLookup func(name string) (providers.Provider, bool)

func responseWithProvider(resp *providers.Response, provider string) *providers.Response {
	if resp == nil || resp.Provider != "" {
		return resp
	}
	resp.Provider = provider
	return resp
}
