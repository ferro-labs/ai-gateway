package strategies

import (
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
)

// lbCounts tallies the weight-biased FIRST pick over n draws. Under
// mode: loadbalance the pipeline commits to that key, so it is the whole of what
// the weight decides.
func lbCounts(t *testing.T, lb *LoadBalance, model string, n int) map[string]int {
	t.Helper()
	counts := map[string]int{}
	for range n {
		keys, err := lb.SelectTargets(providers.Request{Model: model})
		if err != nil {
			t.Fatal(err)
		}
		if len(keys) == 0 {
			counts[""]++
			continue
		}
		counts[keys[0]]++
	}
	return counts
}

func TestLoadBalance_SpreadsAcrossTargets(t *testing.T) {
	ma := &mockProvider{name: "a", models: []string{"gpt-4o"}}
	mb := &mockProvider{name: "b", models: []string{"gpt-4o"}}

	lb := NewLoadBalance(
		[]Target{{VirtualKey: "a", Weight: 50}, {VirtualKey: "b", Weight: 50}},
		newLookup(ma, mb),
	)

	counts := lbCounts(t, lb, "gpt-4o", 200)
	if counts["a"] == 0 || counts["b"] == 0 {
		t.Errorf("both targets should receive traffic, got %v", counts)
	}
}

func TestLoadBalance_NoTargets(t *testing.T) {
	lb := NewLoadBalance(nil, newLookup())
	keys, err := lb.SelectTargets(providers.Request{Model: "gpt-4o"})
	if err != nil {
		t.Fatal(err)
	}
	if keys != nil {
		t.Errorf("expected no candidates, got %v", keys)
	}
}

func TestLoadBalance_FiltersUnsupportedModels(t *testing.T) {
	// "a" serves gpt-4o, "b" does not. Only "a" is a candidate.
	ma := &mockProvider{name: "a", models: []string{"gpt-4o"}}
	mb := &mockProvider{name: "b", models: []string{"claude-3"}}

	lb := NewLoadBalance(
		[]Target{{VirtualKey: "a", Weight: 50}, {VirtualKey: "b", Weight: 50}},
		newLookup(ma, mb),
	)

	counts := lbCounts(t, lb, "gpt-4o", 100)
	if counts["b"] != 0 {
		t.Errorf("target b does not serve the model and must never be picked, got %v", counts)
	}
	if counts["a"] != 100 {
		t.Errorf("target a should take every request, got %v", counts)
	}
}

func TestLoadBalance_NoProviderSupportsModel(t *testing.T) {
	ma := &mockProvider{name: "a", models: []string{"gpt-4o"}}
	lb := NewLoadBalance([]Target{{VirtualKey: "a", Weight: 50}}, newLookup(ma))

	keys, err := lb.SelectTargets(providers.Request{Model: "unknown-model"})
	if err != nil {
		t.Fatalf("nothing serving the model is the caller's 404, not a strategy error: %v", err)
	}
	if keys != nil {
		t.Errorf("expected no candidates, got %v", keys)
	}
}

func TestLoadBalance_RespectsWeights(t *testing.T) {
	ma := &mockProvider{name: "a", models: []string{"gpt-4o"}}
	mb := &mockProvider{name: "b", models: []string{"gpt-4o"}}

	lb := NewLoadBalance(
		[]Target{{VirtualKey: "a", Weight: 90}, {VirtualKey: "b", Weight: 10}},
		newLookup(ma, mb),
	)

	counts := lbCounts(t, lb, "gpt-4o", 1000)
	if counts["a"] < 700 {
		t.Errorf("expected ~900 of 1000 to target a, got %v", counts)
	}
	if counts["b"] == 0 {
		t.Errorf("target b should get some traffic, got %v", counts)
	}
}

// TestLoadBalance_ZeroWeightTargetIsDrained is F14. `weight: 0` used to be
// silently promoted to 1, so the documented way to drain a target before
// revoking its credential kept sending it traffic — measured at 3 of 300 —
// precisely when a request to it was about to start failing.
func TestLoadBalance_ZeroWeightTargetIsDrained(t *testing.T) {
	drained := &mockProvider{name: "drained", models: []string{"gpt-4o"}}
	live := &mockProvider{name: "live", models: []string{"gpt-4o"}}

	lb := NewLoadBalance(
		[]Target{{VirtualKey: "drained", Weight: 0}, {VirtualKey: "live", Weight: 100}},
		newLookup(drained, live),
	)

	counts := lbCounts(t, lb, "gpt-4o", 2000)
	if counts["drained"] != 0 {
		t.Errorf("drained target received %d of 2000, want 0", counts["drained"])
	}
	if counts["live"] != 2000 {
		t.Errorf("live target received %d of 2000, want 2000", counts["live"])
	}
}

// TestLoadBalance_ZeroWeightTargetIsDrainedEvenWhenLast guards the
// floating-point tail of weightedStartIndex: the final index must not be handed
// the request just for being final.
func TestLoadBalance_ZeroWeightTargetIsDrainedEvenWhenLast(t *testing.T) {
	live := &mockProvider{name: "live", models: []string{"gpt-4o"}}
	drained := &mockProvider{name: "drained", models: []string{"gpt-4o"}}

	lb := NewLoadBalance(
		[]Target{{VirtualKey: "live", Weight: 100}, {VirtualKey: "drained", Weight: 0}},
		newLookup(live, drained),
	)

	counts := lbCounts(t, lb, "gpt-4o", 2000)
	if counts["drained"] != 0 {
		t.Errorf("drained target received %d of 2000, want 0", counts["drained"])
	}
}

// TestLoadBalance_AllCompatibleTargetsDrainedSelectsNothing: when the only
// targets that serve the model are drained, the answer is "nothing here serves
// this", not "use a drained one anyway".
func TestLoadBalance_AllCompatibleTargetsDrainedSelectsNothing(t *testing.T) {
	drained := &mockProvider{name: "drained", models: []string{"gpt-4o"}}
	other := &mockProvider{name: "other", models: []string{"claude-3"}}

	lb := NewLoadBalance(
		[]Target{{VirtualKey: "drained", Weight: 0}, {VirtualKey: "other", Weight: 100}},
		newLookup(drained, other),
	)

	keys, err := lb.SelectTargets(providers.Request{Model: "gpt-4o"})
	if err != nil {
		t.Fatal(err)
	}
	if keys != nil {
		t.Errorf("expected no candidates, got %v", keys)
	}
}
