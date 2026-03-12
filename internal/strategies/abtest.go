package strategies

import (
	"context"
	"fmt"
	"math/rand"
	"sync"

	"github.com/ferro-labs/ai-gateway/internal/logging"
	"github.com/ferro-labs/ai-gateway/providers"
)

// ABTestVariant defines a routing target with an associated traffic weight and
// a human-readable label used for analytics and logging.
type ABTestVariant struct {
	// Target is the provider to route matching traffic to.
	Target Target
	// Weight is the relative share of traffic this variant receives.
	// All variant weights are summed; each variant's share is Weight/Total.
	// Zero-weight variants may be normalised to 1 (equal distribution).
	Weight float64
	// Label is a short identifier for this variant, e.g. "control", "challenger".
	// It is emitted as a structured log field on every routed request so that
	// operators can correlate variant assignments in their observability stack.
	Label string
}

// ABTest implements weighted random traffic splitting across labelled variants.
//
// Use this strategy to compare model quality or cost during a gradual migration:
//
//	strategy:
//	  mode: ab-test
//	  ab_variants:
//	    - target_key: gpt-4o
//	      weight: 80
//	      label: control
//	    - target_key: claude-3-5-sonnet
//	      weight: 20
//	      label: challenger
//
// The selected variant is logged with field "ab_variant" on every request.
// All traffic still goes to a real provider — this is not a shadow-traffic mode.
type ABTest struct {
	variants []ABTestVariant
	lookup   ProviderLookup
	mu       sync.Mutex
}

// NewABTest creates an ABTest strategy.
//
// Returns an error when no variants are provided or any variant has a negative weight.
// Zero-weight variants are treated as weight 1 (equal distribution) at routing time.
func NewABTest(variants []ABTestVariant, lookup ProviderLookup) (*ABTest, error) {
	if len(variants) == 0 {
		return nil, fmt.Errorf("ab-test: at least one variant is required")
	}
	for _, v := range variants {
		if v.Weight < 0 {
			return nil, fmt.Errorf("ab-test: variant %q has negative weight %.2f", v.Label, v.Weight)
		}
	}
	return &ABTest{variants: variants, lookup: lookup}, nil
}

// Execute selects a variant by weighted random sampling, routes the request to
// its target provider, and logs the selected variant label.
func (ab *ABTest) Execute(ctx context.Context, req providers.Request) (*providers.Response, error) {
	variant, err := ab.selectVariant()
	if err != nil {
		return nil, err
	}

	logging.Logger.Info("ab-test variant selected",
		"ab_variant", variant.Label,
		"target", variant.Target.VirtualKey,
		"model", req.Model,
	)

	p, ok := ab.lookup(variant.Target.VirtualKey)
	if !ok {
		return nil, fmt.Errorf("ab-test: provider not found: %s", variant.Target.VirtualKey)
	}
	return p.Complete(ctx, req)
}

// selectVariant picks a variant using weighted random sampling.
// Variants with zero weight are treated as weight 1 (equal distribution).
func (ab *ABTest) selectVariant() (ABTestVariant, error) {
	ab.mu.Lock()
	defer ab.mu.Unlock()

	total := 0.0
	for _, v := range ab.variants {
		w := effectiveWeight(v.Weight)
		total += w
	}
	if total == 0 {
		return ABTestVariant{}, fmt.Errorf("ab-test: no eligible variants")
	}

	r := rand.Float64() * total //nolint:gosec
	cumulative := 0.0
	for _, v := range ab.variants {
		cumulative += effectiveWeight(v.Weight)
		if r < cumulative {
			return v, nil
		}
	}
	// Floating-point rounding safety net — return last variant.
	return ab.variants[len(ab.variants)-1], nil
}

// effectiveWeight returns the weight to use for a variant:
// zero-weight variants participate as weight 1 (equal distribution).
func effectiveWeight(w float64) float64 {
	if w <= 0 {
		return 1
	}
	return w
}
