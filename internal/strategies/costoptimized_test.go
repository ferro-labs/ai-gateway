package strategies

import (
	"context"
	"testing"

	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/providers"
)

// ptrF returns a *float64 from a literal — helper for building catalog fixtures.
func ptrF(v float64) *float64 { return &v }

// buildCatalog creates a minimal Catalog with two providers having different
// per-token prices for gpt-4o.
func buildCatalog() models.Catalog {
	return models.Catalog{
		// Cheap provider: $1 / 1M input tokens.
		"cheap/gpt-4o": {
			Provider: "cheap",
			ModelID:  "gpt-4o",
			Mode:     models.ModeChat,
			Pricing: models.Pricing{
				InputPerMTokens:  ptrF(1.0),
				OutputPerMTokens: ptrF(2.0),
			},
		},
		// Expensive provider: $10 / 1M input tokens.
		"expensive/gpt-4o": {
			Provider: "expensive",
			ModelID:  "gpt-4o",
			Mode:     models.ModeChat,
			Pricing: models.Pricing{
				InputPerMTokens:  ptrF(10.0),
				OutputPerMTokens: ptrF(20.0),
			},
		},
	}
}

func TestCostOptimized_PicksCheapest(t *testing.T) {
	cheap := &mockProvider{name: "cheap", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "cheap"}}
	expensive := &mockProvider{name: "expensive", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "expensive"}}

	catalog := buildCatalog()
	targets := []Target{{VirtualKey: "cheap"}, {VirtualKey: "expensive"}}
	s := NewCostOptimized(targets, newLookup(cheap, expensive), catalog)

	resp, err := s.Execute(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hello world"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "cheap" {
		t.Errorf("expected cheap provider, got %q", resp.ID)
	}
}

func TestCostOptimized_FallsBackWhenNoPricing(t *testing.T) {
	// Catalog has no entry for "unknown/gpt-4o".
	mp := &mockProvider{name: "unknown", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "ok"}}
	targets := []Target{{VirtualKey: "unknown"}}
	s := NewCostOptimized(targets, newLookup(mp), models.Catalog{})

	resp, err := s.Execute(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "ok" {
		t.Errorf("expected ok, got %q", resp.ID)
	}
}

func TestCostOptimized_SkipsUnsupportedModel(t *testing.T) {
	p1 := &mockProvider{name: "cheap", models: []string{"gpt-3.5-turbo"}, resp: &providers.Response{ID: "wrong"}}
	p2 := &mockProvider{name: "expensive", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "right"}}

	catalog := buildCatalog()
	targets := []Target{{VirtualKey: "cheap"}, {VirtualKey: "expensive"}}
	s := NewCostOptimized(targets, newLookup(p1, p2), catalog)

	resp, err := s.Execute(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "right" {
		t.Errorf("only expensive supports gpt-4o, expected right, got %q", resp.ID)
	}
}

func TestCostOptimized_NoTargets(t *testing.T) {
	s := NewCostOptimized(nil, newLookup(), models.Catalog{})
	_, err := s.Execute(context.Background(), providers.Request{Model: "gpt-4o"})
	if err == nil {
		t.Fatal("expected error for no targets")
	}
}
