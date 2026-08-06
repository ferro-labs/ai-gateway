package strategies

import (
	"slices"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
)

// abTestModel is the model every variant in this file serves, so a test that is
// not about eligibility never has its split narrowed by it.
const abTestModel = "gpt-4o"

// abTestLookup registers one provider per key, each serving abTestModel.
func abTestLookup(keys ...string) ProviderLookup {
	pp := make([]providers.Provider, 0, len(keys))
	for _, k := range keys {
		pp = append(pp, &mockProvider{name: k, models: []string{abTestModel}})
	}
	return newLookup(pp...)
}

func TestABTest_NewABTest_NoVariants(t *testing.T) {
	_, err := NewABTest(nil, nil)
	if err == nil {
		t.Fatal("expected error when no variants provided")
	}
}

func TestABTest_NewABTest_ZeroTotalWeight(t *testing.T) {
	variants := []ABTestVariant{
		{Target: Target{VirtualKey: "a"}, Weight: 0, Label: "a"},
	}
	// Zero means zero, so an all-zero variant set selects nothing at all and is
	// rejected here rather than becoming an equal split nobody asked for.
	if _, err := NewABTest(variants, nil); err == nil {
		t.Fatal("expected error when every variant weight is zero")
	}
}

func TestABTest_NewABTest_NegativeWeight(t *testing.T) {
	variants := []ABTestVariant{
		{Target: Target{VirtualKey: "a"}, Weight: -1, Label: "a"},
	}
	_, err := NewABTest(variants, nil)
	if err == nil {
		t.Fatal("expected error for negative weight")
	}
}

func TestABTest_SelectTargets_SingleVariant(t *testing.T) {
	ab, err := NewABTest([]ABTestVariant{
		{Target: Target{VirtualKey: "only"}, Weight: 1, Label: "control"},
	}, abTestLookup("only"))
	if err != nil {
		t.Fatal(err)
	}
	keys, err := ab.SelectTargets(providers.Request{Model: abTestModel})
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0] != "only" {
		t.Fatalf("got %v, want [only]", keys)
	}
}

func TestABTest_SelectTargets_TrafficDistribution(t *testing.T) {
	ab, err := NewABTest([]ABTestVariant{
		{Target: Target{VirtualKey: "a"}, Weight: 90, Label: "control"},
		{Target: Target{VirtualKey: "b"}, Weight: 10, Label: "challenger"},
	}, abTestLookup("a", "b"))
	if err != nil {
		t.Fatal(err)
	}
	ab.WithRoutingTargets([]Target{{VirtualKey: "a"}, {VirtualKey: "b"}})

	counts := selectionCounts(t, ab, 10_000)
	aShare := float64(counts["a"]) / 10_000
	if aShare < 0.87 || aShare > 0.93 {
		t.Errorf("expected ~90%% traffic to variant a, got %.1f%% (%d)", aShare*100, counts["a"])
	}
}

// TestABTest_SelectTargets_ZeroWeightVariantIsDrained is F15's sibling: a
// variant weighted 0 must receive nothing. It used to be silently promoted to
// weight 1, so "drain this variant" and "give it an equal share" were the same
// config.
func TestABTest_SelectTargets_ZeroWeightVariantIsDrained(t *testing.T) {
	ab, err := NewABTest([]ABTestVariant{
		{Target: Target{VirtualKey: "drained"}, Weight: 0, Label: "old"},
		{Target: Target{VirtualKey: "live"}, Weight: 100, Label: "new"},
	}, abTestLookup("drained", "live"))
	if err != nil {
		t.Fatal(err)
	}
	ab.WithRoutingTargets([]Target{{VirtualKey: "drained"}, {VirtualKey: "live"}})

	counts := selectionCounts(t, ab, 10_000)
	if counts["drained"] != 0 {
		t.Errorf("drained variant received %d of 10000 selections, want 0", counts["drained"])
	}
	if counts["live"] != 10_000 {
		t.Errorf("live variant received %d of 10000 selections, want 10000", counts["live"])
	}
}

// TestABTest_SelectTargets_ModelEligibility covers the draw being confined to
// the variants that can serve the request.
//
// The pipeline commits to a single-target strategy's pick instead of falling
// through it, so drawing a variant that cannot serve the model answered 404 —
// and drawing it at random answered 404 at random, which made a model the
// gateway advertises work roughly half the time.
//
// wantFirst lists the keys the draw is allowed to yield first over many
// iterations; wantNone asserts the strategy offers no candidate at all.
func TestABTest_SelectTargets_ModelEligibility(t *testing.T) {
	const iterations = 400

	variants := []ABTestVariant{
		{Target: Target{VirtualKey: "a"}, Weight: 1, Label: "control"},
		{Target: Target{VirtualKey: "b"}, Weight: 1, Label: "challenger"},
	}

	tests := []struct {
		name      string
		model     string
		providers []providers.Provider
		wantFirst []string
		wantNone  bool
	}{
		{
			name:  "only one variant serves the model",
			model: "b-only",
			providers: []providers.Provider{
				&mockProvider{name: "a", models: []string{"shared"}},
				&mockProvider{name: "b", models: []string{"shared", "b-only"}},
			},
			wantFirst: []string{"b"},
		},
		{
			name:  "both variants serve the model",
			model: "shared",
			providers: []providers.Provider{
				&mockProvider{name: "a", models: []string{"shared"}},
				&mockProvider{name: "b", models: []string{"shared"}},
			},
			wantFirst: []string{"a", "b"},
		},
		{
			name:  "a variant whose provider is not registered is excluded",
			model: "shared",
			providers: []providers.Provider{
				&mockProvider{name: "b", models: []string{"shared"}},
			},
			wantFirst: []string{"b"},
		},
		{
			name:  "no variant serves the model",
			model: "nobody",
			providers: []providers.Provider{
				&mockProvider{name: "a", models: []string{"shared"}},
				&mockProvider{name: "b", models: []string{"shared"}},
			},
			wantNone: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ab, err := NewABTest(variants, newLookup(tt.providers...))
			if err != nil {
				t.Fatal(err)
			}
			ab.WithRoutingTargets([]Target{{VirtualKey: "a"}, {VirtualKey: "b"}})

			seen := map[string]int{}
			for range iterations {
				keys, err := ab.SelectTargets(providers.Request{Model: tt.model})
				if err != nil {
					t.Fatalf("SelectTargets = %v, want no error", err)
				}
				if tt.wantNone {
					if len(keys) != 0 {
						t.Fatalf("SelectTargets = %v, want no candidates for a model no variant serves", keys)
					}
					continue
				}
				if len(keys) == 0 {
					t.Fatal("SelectTargets returned no keys")
				}
				seen[keys[0]]++
			}
			if tt.wantNone {
				return
			}
			for key := range seen {
				if !slices.Contains(tt.wantFirst, key) {
					t.Errorf("draw yielded %q, want one of %v — a variant that cannot serve the model must never be drawn", key, tt.wantFirst)
				}
			}
			// Every eligible variant must still be reachable, so the filter
			// narrows the draw without collapsing it onto one arm.
			for _, key := range tt.wantFirst {
				if seen[key] == 0 {
					t.Errorf("variant %q was never drawn in %d iterations, want it to keep its share", key, iterations)
				}
			}
		})
	}
}

// selectionCounts tallies the FIRST key of each SelectTargets answer, which is
// the target the pipeline commits to for a non-fallback mode.
func selectionCounts(t *testing.T, s Strategy, iterations int) map[string]int {
	t.Helper()
	counts := map[string]int{}
	for range iterations {
		keys, err := s.SelectTargets(providers.Request{Model: abTestModel})
		if err != nil {
			t.Fatal(err)
		}
		if len(keys) == 0 {
			t.Fatal("SelectTargets returned no keys")
		}
		counts[keys[0]]++
	}
	return counts
}
