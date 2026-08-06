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
	tr.Record("fast", 20*time.Millisecond)
	tr.Record("slow", 200*time.Millisecond)

	targets := []Target{{VirtualKey: "fast"}, {VirtualKey: "slow"}}
	s := NewLeastLatency(targets, newLookup(fast, slow), tr)

	assertLeadsWith(t, s, providers.Request{Model: "gpt-4o"}, "fast")
}

func TestLeastLatency_UnseenTargetsLeadSoTheyGetProfiled(t *testing.T) {
	seen := &mockProvider{name: "seen", models: []string{"gpt-4o"}}
	unseen := &mockProvider{name: "unseen", models: []string{"gpt-4o"}}

	tr := latency.New(10)
	tr.Record("seen", 1*time.Millisecond) // fastest known, but still yields

	targets := []Target{{VirtualKey: "seen"}, {VirtualKey: "unseen"}}
	s := NewLeastLatency(targets, newLookup(seen, unseen), tr)

	assertLeadsWith(t, s, providers.Request{Model: "gpt-4o"}, "unseen")
}

func TestLeastLatency_SkipsUnsupportedModel(t *testing.T) {
	p1 := &mockProvider{name: "p1", models: []string{"gpt-3.5-turbo"}}
	p2 := &mockProvider{name: "p2", models: []string{"gpt-4o"}}

	tr := latency.New(10)
	tr.Record("p1", 10*time.Millisecond) // p1 is "faster" but does not serve gpt-4o
	tr.Record("p2", 100*time.Millisecond)

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
