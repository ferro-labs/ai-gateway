package strategies

import (
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/latency"
	"github.com/ferro-labs/ai-gateway/providers"
)

func TestLeastLatency_PicksFastest(t *testing.T) {
	fast := &mockProvider{name: "fast", models: []string{"gpt-4o"}}
	slow := &mockProvider{name: "slow", models: []string{"gpt-4o"}}

	tr := latency.New(10)
	tr.Record("fast", "gpt-4o", 20*time.Millisecond)
	tr.Record("slow", "gpt-4o", 200*time.Millisecond)

	targets := []Target{{VirtualKey: "fast"}, {VirtualKey: "slow"}}
	s := NewLeastLatency(targets, newLookup(fast, slow), tr)

	assertLeadsWith(t, s, providers.Request{Model: "gpt-4o"}, "fast")
}

func TestLeastLatency_UnseenTargetsLeadSoTheyGetProfiled(t *testing.T) {
	seen := &mockProvider{name: "seen", models: []string{"gpt-4o"}}
	unseen := &mockProvider{name: "unseen", models: []string{"gpt-4o"}}

	tr := latency.New(10)
	tr.Record("seen", "gpt-4o", 1*time.Millisecond) // fastest known, but still yields

	targets := []Target{{VirtualKey: "seen"}, {VirtualKey: "unseen"}}
	s := NewLeastLatency(targets, newLookup(seen, unseen), tr)

	assertLeadsWith(t, s, providers.Request{Model: "gpt-4o"}, "unseen")
}

func TestLeastLatency_SkipsUnsupportedModel(t *testing.T) {
	p1 := &mockProvider{name: "p1", models: []string{"gpt-3.5-turbo"}}
	p2 := &mockProvider{name: "p2", models: []string{"gpt-4o"}}

	tr := latency.New(10)
	tr.Record("p1", "gpt-4o", 10*time.Millisecond) // p1 is "faster" but does not serve gpt-4o
	tr.Record("p2", "gpt-4o", 100*time.Millisecond)

	targets := []Target{{VirtualKey: "p1"}, {VirtualKey: "p2"}}
	s := NewLeastLatency(targets, newLookup(p1, p2), tr)

	// p1 has the better p50 but does not serve gpt-4o, so p2 must lead. p1 still
	// trails as a declared fallback; the pipeline skips it on model support.
	assertLeadsWith(t, s, providers.Request{Model: "gpt-4o"}, "p2")
}

func TestLeastLatency_NoTargets(t *testing.T) {
	s := NewLeastLatency(nil, newLookup(), latency.New(10))
	keys, err := s.SelectTargets(providers.Request{Model: "gpt-4o"})
	if err != nil {
		t.Fatal(err)
	}
	if keys != nil {
		t.Errorf("expected no candidates with no targets, got %v", keys)
	}
}

// Once every target is sampled the leader would otherwise lock in: nothing
// but its own samples ever changes, so a sibling that got faster is never
// seen. A bounded share of requests leads with a sampled non-leader instead.
func TestLeastLatency_ExplorationIsBounded(t *testing.T) {
	a := &mockProvider{name: "a", models: []string{"gpt-4o"}}
	b := &mockProvider{name: "b", models: []string{"gpt-4o"}}
	c := &mockProvider{name: "c", models: []string{"gpt-4o"}}
	tr := latency.New(10)
	tr.Record("a", "gpt-4o", 10*time.Millisecond)
	tr.Record("b", "gpt-4o", 50*time.Millisecond)
	tr.Record("c", "gpt-4o", 90*time.Millisecond)
	s := NewLeastLatency([]Target{{VirtualKey: "a"}, {VirtualKey: "b"}, {VirtualKey: "c"}}, newLookup(a, b, c), tr)

	const draws = 4000
	explored := 0
	for range draws {
		keys, err := s.SelectTargets(providers.Request{Model: "gpt-4o"})
		if err != nil {
			t.Fatal(err)
		}
		if keys[0] != "a" {
			explored++
		}
	}
	share := float64(explored) / draws
	if share < 0.05 || share > 0.16 {
		t.Fatalf("non-leader led %.1f%% of requests, want about %.0f%%", share*100, explorationShare*100)
	}
}

// A target that was fastest and slowed down loses leadership as its window
// fills with the new numbers.
func TestLeastLatency_SlowedLeaderLosesLeadership(t *testing.T) {
	a := &mockProvider{name: "a", models: []string{"gpt-4o"}}
	b := &mockProvider{name: "b", models: []string{"gpt-4o"}}
	tr := latency.New(4)
	tr.Record("b", "gpt-4o", 50*time.Millisecond)
	for range 4 {
		tr.Record("a", "gpt-4o", 10*time.Millisecond)
	}
	s := NewLeastLatency([]Target{{VirtualKey: "a"}, {VirtualKey: "b"}}, newLookup(a, b), tr)
	if leader := leaderOver(t, s, "gpt-4o", 200); leader != "a" {
		t.Fatalf("leader = %q before the slowdown, want a", leader)
	}
	for range 4 {
		tr.Record("a", "gpt-4o", 500*time.Millisecond)
	}
	if leader := leaderOver(t, s, "gpt-4o", 200); leader != "b" {
		t.Fatalf("leader = %q after a slowed down, want b", leader)
	}
}

// leaderOver returns the key that led the most of n selections, so a test
// reads the ranking through the exploration share rather than being flaked by it.
func leaderOver(t *testing.T, s Strategy, model string, n int) string {
	t.Helper()
	counts := map[string]int{}
	for range n {
		keys, err := s.SelectTargets(providers.Request{Model: model})
		if err != nil {
			t.Fatal(err)
		}
		counts[keys[0]]++
	}
	leader, best := "", -1
	for k, c := range counts {
		if c > best {
			leader, best = k, c
		}
	}
	return leader
}

// History older than the sample TTL is not a p50. The target reads as unseen,
// so it is re-profiled rather than ranked on a number that may be an incident old.
func TestLeastLatency_ExpiredHistoryDoesNotRank(t *testing.T) {
	stale := &mockProvider{name: "stale", models: []string{"gpt-4o"}}
	fresh := &mockProvider{name: "fresh", models: []string{"gpt-4o"}}
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	tr := latency.New(10, latency.WithSampleTTL(time.Minute), latency.WithClock(func() time.Time { return now }))
	tr.Record("stale", "gpt-4o", 1*time.Millisecond)
	now = now.Add(5 * time.Minute)
	tr.Record("fresh", "gpt-4o", 80*time.Millisecond)
	s := NewLeastLatency([]Target{{VirtualKey: "fresh"}, {VirtualKey: "stale"}}, newLookup(stale, fresh), tr)

	keys, err := s.SelectTargets(providers.Request{Model: "gpt-4o"})
	if err != nil {
		t.Fatal(err)
	}
	// stale leads as UNSEEN — to be measured again — not on its 1ms from five
	// minutes ago; the assertion that matters is that the expired sample is
	// gone, which the tracker reports directly.
	if _, ok := tr.Stats("stale", "gpt-4o"); ok {
		t.Fatal("expired history still reads as samples")
	}
	assertKeys(t, keys, "stale", "fresh")
}

// Samples are keyed by target and upstream model. One target serving a slow
// model and a fast one through model_map ranks each on its own numbers.
func TestLeastLatency_MappedModelsDoNotContaminateEachOther(t *testing.T) {
	shared := &mockProvider{name: "shared", models: []string{"visible"}}
	other := &mockProvider{name: "other", models: []string{"visible"}}
	tr := latency.New(10)
	tr.Record("shared", "slow-upstream", 900*time.Millisecond)
	tr.Record("shared", "fast-upstream", 5*time.Millisecond)
	tr.Record("other", "visible", 50*time.Millisecond)

	targets := []Target{
		{VirtualKey: "shared", ModelMap: map[string]string{"visible": "fast-upstream"}},
		{VirtualKey: "other"},
	}
	s := NewLeastLatency(targets, newLookup(shared, other), tr)
	if leader := leaderOver(t, s, "visible", 200); leader != "shared" {
		t.Fatalf("leader = %q, want shared: its fast-upstream samples must rank it, not slow-upstream's", leader)
	}
}
