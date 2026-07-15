package strategies

import (
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
)

func TestLoadBalance_SelectTargets_MatchesExecuteCandidates(t *testing.T) {
	tests := []struct {
		name    string
		targets []Target
		provs   []providers.Provider
		model   string
		wantSet []string // exact set SelectTargets may return (order-independent)
	}{
		{
			name: "excludes incompatible high-weight target",
			targets: []Target{
				{VirtualKey: "a", Weight: 1},
				{VirtualKey: "x", Weight: 100},
				{VirtualKey: "b", Weight: 1},
			},
			provs: []providers.Provider{
				&mockProvider{name: "a", models: []string{"gpt-4o"}},
				&mockProvider{name: "x", models: []string{"claude-3"}},
				&mockProvider{name: "b", models: []string{"gpt-4o"}},
			},
			model:   "gpt-4o",
			wantSet: []string{"a", "b"},
		},
		{
			name: "excludes unregistered high-weight target",
			targets: []Target{
				{VirtualKey: "a", Weight: 1},
				{VirtualKey: "missing", Weight: 100},
			},
			provs:   []providers.Provider{&mockProvider{name: "a", models: []string{"gpt-4o"}}},
			model:   "gpt-4o",
			wantSet: []string{"a"},
		},
		{
			name:    "no compatible target returns empty",
			targets: []Target{{VirtualKey: "x", Weight: 100}},
			provs:   []providers.Provider{&mockProvider{name: "x", models: []string{"claude-3"}}},
			model:   "gpt-4o",
			wantSet: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lb := NewLoadBalance(tt.targets, newLookup(tt.provs...))
			r := providers.Request{Model: tt.model, Messages: []providers.Message{{Role: "user", Content: "hi"}}}
			want := make(map[string]bool, len(tt.wantSet))
			for _, k := range tt.wantSet {
				want[k] = true
			}
			// Weighted rotation is random, so sample many draws: the returned set
			// and the weight-biased first pick must always come from the compatible
			// candidates, never the excluded high-weight target.
			for i := 0; i < 500; i++ {
				keys, err := lb.SelectTargets(r)
				if err != nil {
					t.Fatalf("SelectTargets: %v", err)
				}
				if len(keys) != len(tt.wantSet) {
					t.Fatalf("SelectTargets = %v, want set %v", keys, tt.wantSet)
				}
				for _, k := range keys {
					if !want[k] {
						t.Fatalf("SelectTargets = %v, contains excluded key %q", keys, k)
					}
				}
			}
		})
	}
}

// TestConditional_SelectTargets_FallbackWithoutRoutingTargets asserts that a
// Conditional built without WithRoutingTargets still surfaces the fallback that
// Execute routes to, rather than an empty streaming list.
