package strategies

import (
	"context"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/latency"
	"github.com/ferro-labs/ai-gateway/providers"
)

func TestLeastLatency_PicksFastest(t *testing.T) {
	fast := &mockProvider{name: "fast", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "fast"}}
	slow := &mockProvider{name: "slow", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "slow"}}

	tr := latency.New(10)
	tr.Record("fast", 20*time.Millisecond)
	tr.Record("slow", 200*time.Millisecond)

	targets := []Target{{VirtualKey: "fast"}, {VirtualKey: "slow"}}
	s := NewLeastLatency(targets, newLookup(fast, slow), tr)

	resp, err := s.Execute(context.Background(), providers.Request{Model: "gpt-4o"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "fast" {
		t.Errorf("expected fast provider, got %q", resp.ID)
	}
}

func TestLeastLatency_FallsBackToRandom_WhenNoSamples(t *testing.T) {
	mp := &mockProvider{name: "p1", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "ok"}}

	tr := latency.New(10) // no samples recorded
	targets := []Target{{VirtualKey: "p1"}}
	s := NewLeastLatency(targets, newLookup(mp), tr)

	_, err := s.Execute(context.Background(), providers.Request{Model: "gpt-4o"})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
}

func TestLeastLatency_SkipsUnsupportedModel(t *testing.T) {
	p1 := &mockProvider{name: "p1", models: []string{"gpt-3.5-turbo"}, resp: &providers.Response{ID: "p1"}}
	p2 := &mockProvider{name: "p2", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "p2"}}

	tr := latency.New(10)
	tr.Record("p1", 10*time.Millisecond) // p1 is "faster" but doesn't support gpt-4o
	tr.Record("p2", 100*time.Millisecond)

	targets := []Target{{VirtualKey: "p1"}, {VirtualKey: "p2"}}
	s := NewLeastLatency(targets, newLookup(p1, p2), tr)

	resp, err := s.Execute(context.Background(), providers.Request{Model: "gpt-4o"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "p2" {
		t.Errorf("expected p2 (only one supporting gpt-4o), got %q", resp.ID)
	}
}

func TestLeastLatency_NoTargets(t *testing.T) {
	tr := latency.New(10)
	s := NewLeastLatency(nil, newLookup(), tr)
	_, err := s.Execute(context.Background(), providers.Request{Model: "gpt-4o"})
	if err == nil {
		t.Fatal("expected error for no targets")
	}
}
