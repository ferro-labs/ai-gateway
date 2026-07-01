package strategies

import (
	"context"
	"fmt"
	"math/rand"

	"github.com/ferro-labs/ai-gateway/providers"
)

// dispatch resolves target via lookup, forwards the request to the provider's
// Complete, and stamps the response provider. notFoundPrefix is prefixed to the
// target key when the provider cannot be resolved.
func dispatch(ctx context.Context, lookup ProviderLookup, target Target, req providers.Request, notFoundPrefix string) (*providers.Response, error) {
	p, ok := lookup(target.VirtualKey)
	if !ok {
		return nil, fmt.Errorf("%s: %s", notFoundPrefix, target.VirtualKey)
	}
	resp, err := p.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	return responseWithProvider(resp, target.VirtualKey), nil
}

// weightedPick selects an element by weighted random sampling using weight for
// each element's share. Returns false when the total weight is zero.
func weightedPick[T any](items []T, weight func(T) float64) (T, bool) {
	total := 0.0
	for _, it := range items {
		total += weight(it)
	}
	if total == 0 {
		var zero T
		return zero, false
	}

	r := rand.Float64() * total //nolint:gosec
	cumulative := 0.0
	for _, it := range items {
		cumulative += weight(it)
		if r < cumulative {
			return it, true
		}
	}
	return items[len(items)-1], true
}
