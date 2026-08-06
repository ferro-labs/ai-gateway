package strategies

import (
	"testing"

	"github.com/ferro-labs/ai-gateway/internal/latency"
	"github.com/ferro-labs/ai-gateway/providers"
)

// strategyCases builds every strategy over one target, so a strategy added to
// the package is added here too.
//
// filters records whether the strategy asks the lookup which targets serve the
// model. That split is the whole subject of this file, and it is deliberate
// rather than an inconsistency: a strategy whose choice DEPENDS on the target
// set — cheapest, fastest, weighted — has to consult it, so it can rule the
// model out. A strategy whose choice does not — single, declared order, a
// matched rule — names its target and lets the pipeline's resolveTarget decide
// whether that target serves the model. Both roads end at the same 404; only one
// of them can end there before a provider is named.
func strategyCases(targets []Target) []struct {
	name    string
	filters bool
	build   func(t *testing.T, lookup ProviderLookup) Strategy
} {
	return []struct {
		name    string
		filters bool
		build   func(t *testing.T, lookup ProviderLookup) Strategy
	}{
		{"single", false, func(_ *testing.T, _ ProviderLookup) Strategy {
			return NewSingle(targets[0])
		}},
		{"fallback", false, func(_ *testing.T, _ ProviderLookup) Strategy {
			return NewFallback(targets)
		}},
		{"loadbalance", true, func(_ *testing.T, lookup ProviderLookup) Strategy {
			return NewLoadBalance(targets, lookup)
		}},
		{"least-latency", true, func(_ *testing.T, lookup ProviderLookup) Strategy {
			return NewLeastLatency(targets, lookup, latency.New(10))
		}},
		{"cost-optimized", true, func(_ *testing.T, lookup ProviderLookup) Strategy {
			return NewCostOptimized(targets, lookup, buildCatalog())
		}},
		{"conditional", false, func(_ *testing.T, _ ProviderLookup) Strategy {
			return NewConditional(nil, targets[0])
		}},
		{"content-based", false, func(t *testing.T, _ ProviderLookup) Strategy {
			s, err := NewContentBased(nil, targets[0])
			if err != nil {
				t.Fatal(err)
			}
			return s
		}},
		{"ab-test", true, func(t *testing.T, lookup ProviderLookup) Strategy {
			s, err := NewABTest([]ABTestVariant{{Target: targets[0], Weight: 1, Label: "control"}}, lookup)
			if err != nil {
				t.Fatal(err)
			}
			return s
		}},
	}
}

// TestSelectTargets_UnroutableModel asserts that a strategy which filters on
// model support offers NO candidate for a model none of its targets serves.
// routeTargets turns an empty candidate walk into core.ErrNoCapableProvider —
// the caller's 404 — so a strategy that instead offered a target it had already
// ruled out would send the prompt body to a provider that cannot use it.
func TestSelectTargets_UnroutableModel(t *testing.T) {
	targets := []Target{{VirtualKey: "a"}}
	for _, tt := range strategyCases(targets) {
		t.Run(tt.name, func(t *testing.T) {
			p := &mockProvider{name: "a", models: []string{"gpt-4o"}}
			s := tt.build(t, newLookup(p))

			keys, err := s.SelectTargets(providers.Request{
				Model:    "gpt-5",
				Messages: []providers.Message{{Role: "user", Content: "hi"}},
			})
			if err != nil {
				t.Fatalf("SelectTargets = %v, want no error — an unroutable model is the caller's 404", err)
			}
			if tt.filters {
				if keys != nil {
					t.Fatalf("SelectTargets = %v, want no candidates for a model no target serves", keys)
				}
				return
			}
			// The rest name their target and leave the model question to the
			// pipeline, which skips it and reports the same 404.
			assertKeys(t, keys, "a")
		})
	}
}

// TestSelectTargets_NamedTargetLeadsWhenAnotherTargetServesTheModel covers the
// multi-target shape of the same question, which the single-target cases above
// cannot express: a rule names a target that does not serve the request's model
// while a DIFFERENT configured target does.
//
// The named target still leads, and the pipeline commits to the head of a
// committing mode's list — so the request is answered model_not_found even
// though the second target serves the model. That is the rule doing its job:
// naming a target is a routing decision, and the modes that pick by weight or
// by measurement instead rule the model out themselves before naming anything
// (see strategyCases). Pinning the order here is what would catch a silent
// change in either direction.
func TestSelectTargets_NamedTargetLeadsWhenAnotherTargetServesTheModel(t *testing.T) {
	targets := []Target{{VirtualKey: "named"}, {VirtualKey: "other"}}
	lookup := newLookup(
		&mockProvider{name: "named", models: []string{"llama-3.3-70b"}},
		&mockProvider{name: "other", models: []string{"llama-4-maverick"}},
	)
	req := providers.Request{
		Model:    "llama-4-maverick",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	}

	// The premise, asserted rather than assumed: only the second target serves
	// the model, so the leading key below really is the unserving one.
	if routableCandidate(lookup, "named", req.Model) {
		t.Fatal("premise broken: the named target must not serve the model")
	}
	if !routableCandidate(lookup, "other", req.Model) {
		t.Fatal("premise broken: the other target must serve the model")
	}

	cases := []struct {
		name  string
		build func(t *testing.T) Strategy
	}{
		{"conditional", func(*testing.T) Strategy {
			return NewConditional(
				[]ConditionRule{{Key: ConditionKeyModelPrefix, Value: "llama", Target: targets[0]}},
				targets[0],
			).WithRoutingTargets(targets)
		}},
		{"content-based", func(t *testing.T) Strategy {
			s, err := NewContentBased(
				[]ContentRule{{Type: PromptContains, Value: "hi", Target: targets[0]}},
				targets[0],
			)
			if err != nil {
				t.Fatal(err)
			}
			return s.WithRoutingTargets(targets)
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			keys, err := tc.build(t).SelectTargets(req)
			if err != nil {
				t.Fatalf("SelectTargets = %v, want no error", err)
			}
			assertKeys(t, keys, "named", "other")
		})
	}
}

// TestSelectTargets_UnregisteredTarget covers the same shape arriving as a
// missing credential rather than an unknown model: config names a target and no
// provider answers to it.
func TestSelectTargets_UnregisteredTarget(t *testing.T) {
	targets := []Target{{VirtualKey: "ghost"}}
	for _, tt := range strategyCases(targets) {
		t.Run(tt.name, func(t *testing.T) {
			s := tt.build(t, newLookup()) // nothing registered

			keys, err := s.SelectTargets(providers.Request{
				Model:    "gpt-4o",
				Messages: []providers.Message{{Role: "user", Content: "hi"}},
			})
			if err != nil {
				t.Fatalf("SelectTargets = %v, want no error", err)
			}
			if tt.filters {
				if keys != nil {
					t.Fatalf("SelectTargets = %v, want no candidates when nothing is registered", keys)
				}
				return
			}
			assertKeys(t, keys, "ghost")
		})
	}
}
