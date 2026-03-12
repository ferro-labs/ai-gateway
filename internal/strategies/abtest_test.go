package strategies

import (
	"context"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
)

func TestABTest_NewABTest_NoVariants(t *testing.T) {
	_, err := NewABTest(nil, newLookup())
	if err == nil {
		t.Fatal("expected error when no variants provided")
	}
}

func TestABTest_NewABTest_ZeroTotalWeight(t *testing.T) {
	variants := []ABTestVariant{
		{Target: Target{VirtualKey: "a"}, Weight: 0, Label: "a"},
	}
	// Zero weight is normalised to 1 — this should succeed.
	ab, err := NewABTest(variants, newLookup())
	if err != nil {
		t.Fatalf("zero weight should be normalised, got error: %v", err)
	}
	if ab == nil {
		t.Fatal("expected non-nil ABTest")
	}
}

func TestABTest_NewABTest_NegativeWeight(t *testing.T) {
	variants := []ABTestVariant{
		{Target: Target{VirtualKey: "a"}, Weight: -1, Label: "a"},
	}
	_, err := NewABTest(variants, newLookup())
	if err == nil {
		t.Fatal("expected error for negative weight")
	}
}

func TestABTest_Execute_ProviderNotFound(t *testing.T) {
	variants := []ABTestVariant{
		{Target: Target{VirtualKey: "missing"}, Weight: 1, Label: "control"},
	}
	ab, err := NewABTest(variants, newLookup())
	if err != nil {
		t.Fatal(err)
	}

	_, err = ab.Execute(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error when provider not found")
	}
}

func TestABTest_Execute_SingleVariant(t *testing.T) {
	mp := &mockProvider{name: "only", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "ok"}}
	variants := []ABTestVariant{
		{Target: Target{VirtualKey: "only"}, Weight: 1, Label: "control"},
	}
	ab, err := NewABTest(variants, newLookup(mp))
	if err != nil {
		t.Fatal(err)
	}

	resp, err := ab.Execute(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "ok" {
		t.Errorf("got %q, want ok", resp.ID)
	}
}

func TestABTest_Execute_TrafficDistribution(t *testing.T) {
	mpA := &mockProvider{name: "a", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "a"}}
	mpB := &mockProvider{name: "b", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "b"}}
	lookup := newLookup(mpA, mpB)

	variants := []ABTestVariant{
		{Target: Target{VirtualKey: "a"}, Weight: 90, Label: "control"},
		{Target: Target{VirtualKey: "b"}, Weight: 10, Label: "challenger"},
	}
	ab, err := NewABTest(variants, lookup)
	if err != nil {
		t.Fatal(err)
	}

	const iterations = 10_000
	counts := map[string]int{}
	for range iterations {
		resp, err := ab.Execute(context.Background(), providers.Request{
			Model:    "gpt-4o",
			Messages: []providers.Message{{Role: "user", Content: "test"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		counts[resp.ID]++
	}

	// With 90/10 split, "a" should receive ~90% ± 3% tolerance.
	aShare := float64(counts["a"]) / iterations
	if aShare < 0.87 || aShare > 0.93 {
		t.Errorf("expected ~90%% traffic to variant a, got %.1f%% (%d/%d)", aShare*100, counts["a"], iterations)
	}
}

func TestABTest_Execute_EqualWeightDistribution(t *testing.T) {
	mpA := &mockProvider{name: "a", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "a"}}
	mpB := &mockProvider{name: "b", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "b"}}
	lookup := newLookup(mpA, mpB)

	// Both have weight 0 — should be normalised to equal distribution.
	variants := []ABTestVariant{
		{Target: Target{VirtualKey: "a"}, Weight: 0, Label: "a"},
		{Target: Target{VirtualKey: "b"}, Weight: 0, Label: "b"},
	}
	ab, err := NewABTest(variants, lookup)
	if err != nil {
		t.Fatal(err)
	}

	const iterations = 10_000
	counts := map[string]int{}
	for range iterations {
		resp, err := ab.Execute(context.Background(), providers.Request{
			Model:    "gpt-4o",
			Messages: []providers.Message{{Role: "user", Content: "test"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		counts[resp.ID]++
	}

	// Each should receive ~50% ± 5% tolerance.
	aShare := float64(counts["a"]) / iterations
	if aShare < 0.45 || aShare > 0.55 {
		t.Errorf("expected ~50%% traffic to variant a, got %.1f%% (%d/%d)", aShare*100, counts["a"], iterations)
	}
}
