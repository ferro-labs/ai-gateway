package strategies

import (
	"fmt"

	"github.com/ferro-labs/ai-gateway/providers"
)

// ABTestVariant defines a routing target with an associated traffic weight and
// a human-readable label used for analytics and logging.
type ABTestVariant struct {
	// Target is the provider to route matching traffic to.
	Target Target
	// Weight is the relative share of traffic this variant receives.
	// All variant weights are summed; each variant's share is Weight/Total.
	// Zero means zero — a zero-weight variant is never selected.
	Weight float64
	// Label is a short identifier for this variant, e.g. "control", "challenger".
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
// All traffic still goes to a real provider — this is not a shadow-traffic mode.
type ABTest struct {
	variants []ABTestVariant
	targets  []Target
	lookup   ProviderLookup
}

// NewABTest creates an ABTest strategy.
//
// lookup is what keeps the split from turning a listed model into a coin flip:
// the draw runs over the variants that can actually serve the request, the same
// way LoadBalance draws over its compatible targets.
//
// Returns an error when no variants are provided, any variant has a negative
// weight, or every weight is zero. config.ValidateConfig applies the same rules
// at load, so for a validated config this constructor cannot fail; the checks
// remain for a Gateway built from a programmatic Config.
func NewABTest(variants []ABTestVariant, lookup ProviderLookup) (*ABTest, error) {
	if len(variants) == 0 {
		return nil, fmt.Errorf("ab-test: at least one variant is required")
	}
	var sum float64
	for _, v := range variants {
		if v.Weight < 0 {
			return nil, fmt.Errorf("ab-test: variant %q has negative weight %.2f", v.Label, v.Weight)
		}
		sum += v.Weight
	}
	if sum <= 0 {
		return nil, fmt.Errorf("ab-test: variant weights sum to zero; at least one must be positive")
	}
	return &ABTest{variants: variants, lookup: lookup}, nil
}

// eligibleVariants returns the variants whose provider is registered and serves
// model, preserving declared order.
//
// A variant that cannot serve the model is not an arm of the experiment for that
// model: it can never contribute an observation of it, whichever way the draw
// goes. Excluding it therefore distorts no comparison that could have existed,
// and it is the difference between a model one variant serves answering every
// request and answering a random half of them.
//
// A nil lookup keeps every variant, so a Gateway assembled without one behaves as
// it did before the filter existed.
func (ab *ABTest) eligibleVariants(model string) []ABTestVariant {
	if ab.lookup == nil {
		return ab.variants
	}
	eligible := make([]ABTestVariant, 0, len(ab.variants))
	for _, v := range ab.variants {
		if routableCandidate(ab.lookup, v.Target.VirtualKey, model) {
			eligible = append(eligible, v)
		}
	}
	return eligible
}

// WithRoutingTargets records the full ordered target list. SelectTargets appends
// these after the selected variant's target. Returns the receiver so callers can
// chain it after the constructor.
func (ab *ABTest) WithRoutingTargets(targets []Target) *ABTest {
	ab.targets = targets
	return ab
}

// SelectTargets picks a variant by weighted random sampling and returns its
// target followed by every configured target. The pipeline may try the tail
// after a failover-safe failure while retaining attribution to the drawn variant. Variants
// whose provider is missing or does not serve the model are excluded from the
// draw.
//
// Excluding them is what makes the split reproducible per model. The pipeline
// commits to a single-target strategy's pick rather than falling through it, so
// a variant drawn for a model it cannot serve is a 404 — and drawing it half the
// time made a model the gateway advertises succeed half the time. The remedy
// belongs here rather than in that walk: a strategy that NAMES a target from the
// request (single, a matched rule) has made a decision the pipeline must honour,
// while this one draws by weight alone and has made no decision about the model
// to honour.
//
// weightedPick draws from the top-level math/rand source, which is safe for
// concurrent use, so no additional locking is required here.
func (ab *ABTest) SelectTargets(req providers.Request) ([]string, error) {
	v, ok := weightedPick(ab.eligibleVariants(req.Model), func(v ABTestVariant) float64 {
		return positiveWeight(v.Weight)
	})
	if !ok {
		// No variant is both eligible and positively weighted: either no variant
		// serves this model, or every variant that does has been drained to zero.
		// Returning the declared target order here would hand the request to
		// whichever target happens to be first, undoing the drain and reaching a
		// target no variant named; nil says nothing is selectable, which the
		// caller reports as a 404 — and which keeps /v1/models from advertising a
		// model this strategy would refuse.
		return nil, nil
	}
	keys := make([]string, 0, len(ab.targets)+1)
	keys = appendUniqueKey(keys, v.Target.VirtualKey)
	return appendRemainingTargetKeys(keys, ab.targets), nil
}
