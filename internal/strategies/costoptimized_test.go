package strategies

import (
	"strings"
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

// unpricedEntry is a catalog entry that exists but carries no usable pricing.
func unpricedEntry(provider string) models.Model {
	return models.Model{Provider: provider, ModelID: "gpt-4o", Mode: models.ModeChat, Pricing: models.Pricing{}}
}

func TestCostOptimized_PicksCheapest(t *testing.T) {
	cheap := &mockProvider{name: "cheap", models: []string{"gpt-4o"}}
	expensive := &mockProvider{name: "expensive", models: []string{"gpt-4o"}}

	targets := []Target{{VirtualKey: "cheap"}, {VirtualKey: "expensive"}}
	s := NewCostOptimized(targets, newLookup(cheap, expensive), buildCatalog())

	assertLeadsWith(t, s, req("hello world"), "cheap")
}

func TestCostOptimized_FallsBackWhenNoPricing(t *testing.T) {
	// Catalog has no entry for "unknown/gpt-4o".
	mp := &mockProvider{name: "unknown", models: []string{"gpt-4o"}}
	s := NewCostOptimized([]Target{{VirtualKey: "unknown"}}, newLookup(mp), models.Catalog{})

	assertLeadsWith(t, s, req("hi"), "unknown")
}

func TestCostOptimized_SkipsUnpricedCatalogEntryWhenPricedCandidateExists(t *testing.T) {
	unpriced := &mockProvider{name: "unpriced", models: []string{"gpt-4o"}}
	priced := &mockProvider{name: "priced", models: []string{"gpt-4o"}}

	catalog := models.Catalog{
		"unpriced/gpt-4o": unpricedEntry("unpriced"),
		"priced/gpt-4o": {
			Provider: "priced",
			ModelID:  "gpt-4o",
			Mode:     models.ModeChat,
			Pricing: models.Pricing{
				InputPerMTokens:  ptrF(1.0),
				OutputPerMTokens: ptrF(2.0),
			},
		},
	}
	targets := []Target{{VirtualKey: "unpriced"}, {VirtualKey: "priced"}}
	s := NewCostOptimized(targets, newLookup(unpriced, priced), catalog)

	assertLeadsWith(t, s, req("hello"), "priced")
}

func TestCostOptimized_DoesNotRankOutputOnlyPricingAsFreeInput(t *testing.T) {
	outputOnly := &mockProvider{name: "output-only", models: []string{"gpt-4o"}}
	priced := &mockProvider{name: "priced", models: []string{"gpt-4o"}}

	catalog := models.Catalog{
		"output-only/gpt-4o": {
			Provider: "output-only",
			ModelID:  "gpt-4o",
			Mode:     models.ModeChat,
			Pricing:  models.Pricing{OutputPerMTokens: ptrF(2.0)},
		},
		"priced/gpt-4o": {
			Provider: "priced",
			ModelID:  "gpt-4o",
			Mode:     models.ModeChat,
			Pricing: models.Pricing{
				InputPerMTokens:  ptrF(1.0),
				OutputPerMTokens: ptrF(2.0),
			},
		},
	}
	targets := []Target{{VirtualKey: "output-only"}, {VirtualKey: "priced"}}
	s := NewCostOptimized(targets, newLookup(outputOnly, priced), catalog)

	assertLeadsWith(t, s, req("hello"), "priced")
}

func TestCostOptimized_FallsBackToFirstCompatibleWhenAllCandidatesUnpriced(t *testing.T) {
	first := &mockProvider{name: "first", models: []string{"gpt-4o"}}
	second := &mockProvider{name: "second", models: []string{"gpt-4o"}}

	catalog := models.Catalog{
		"first/gpt-4o":  unpricedEntry("first"),
		"second/gpt-4o": unpricedEntry("second"),
	}
	targets := []Target{{VirtualKey: "first"}, {VirtualKey: "second"}}
	s := NewCostOptimized(targets, newLookup(first, second), catalog)

	assertLeadsWith(t, s, req("hello"), "first")
}

func TestCostOptimized_InvalidUnpricedStrategyFallsBackToFirstCompatible(t *testing.T) {
	first := &mockProvider{name: "first", models: []string{"gpt-4o"}}
	second := &mockProvider{name: "second", models: []string{"gpt-4o"}}

	catalog := models.Catalog{
		"first/gpt-4o":  unpricedEntry("first"),
		"second/gpt-4o": unpricedEntry("second"),
	}
	targets := []Target{{VirtualKey: "first"}, {VirtualKey: "second"}}
	s := NewCostOptimized(targets, newLookup(first, second), catalog, "unknown")

	assertLeadsWith(t, s, req("hello"), "first")
}

func TestCostOptimized_AllowTreatsUnpricedAsZeroCost(t *testing.T) {
	priced := &mockProvider{name: "priced", models: []string{"gpt-4o"}}
	unpriced := &mockProvider{name: "unpriced", models: []string{"gpt-4o"}}

	catalog := models.Catalog{
		"priced/gpt-4o": {
			Provider: "priced",
			ModelID:  "gpt-4o",
			Mode:     models.ModeChat,
			Pricing:  models.Pricing{InputPerMTokens: ptrF(1.0)},
		},
		"unpriced/gpt-4o": unpricedEntry("unpriced"),
	}
	targets := []Target{{VirtualKey: "priced"}, {VirtualKey: "unpriced"}}
	s := NewCostOptimized(targets, newLookup(priced, unpriced), catalog, "allow")

	assertLeadsWith(t, s, req("hello"), "unpriced")
}

func TestCostOptimized_SkipErrorsWhenAllCandidatesUnpriced(t *testing.T) {
	first := &mockProvider{name: "first", models: []string{"gpt-4o"}}
	second := &mockProvider{name: "second", models: []string{"gpt-4o"}}

	catalog := models.Catalog{
		"first/gpt-4o":  unpricedEntry("first"),
		"second/gpt-4o": unpricedEntry("second"),
	}
	targets := []Target{{VirtualKey: "first"}, {VirtualKey: "second"}}
	s := NewCostOptimized(targets, newLookup(first, second), catalog, "skip")

	if _, err := s.SelectTargets(req("hello")); err == nil {
		t.Fatal("expected error when skip mode has no priced candidates")
	}
}

func TestCostOptimized_SkipsUnsupportedModel(t *testing.T) {
	p1 := &mockProvider{name: "cheap", models: []string{"gpt-3.5-turbo"}}
	p2 := &mockProvider{name: "expensive", models: []string{"gpt-4o"}}

	targets := []Target{{VirtualKey: "cheap"}, {VirtualKey: "expensive"}}
	s := NewCostOptimized(targets, newLookup(p1, p2), buildCatalog())

	// Only expensive serves gpt-4o, so it must LEAD despite being the dearer of
	// the two. cheap still trails as a declared fallback; the pipeline skips it
	// on model support, which is where that question belongs.
	assertLeadsWith(t, s, req("hi"), "expensive")
}

func TestCostOptimized_NoCandidateWhenNoProviderSupportsModel(t *testing.T) {
	p1 := &mockProvider{name: "cheap", models: []string{"gpt-3.5-turbo"}}
	p2 := &mockProvider{name: "expensive", models: []string{"claude-3"}}

	targets := []Target{{VirtualKey: "cheap"}, {VirtualKey: "expensive"}}
	s := NewCostOptimized(targets, newLookup(p1, p2), buildCatalog())

	keys, err := s.SelectTargets(req("hi"))
	if err != nil {
		t.Fatalf("nothing serving the model is the caller's 404, not a strategy error: %v", err)
	}
	if keys != nil {
		t.Errorf("expected no candidates, got %v", keys)
	}
}

func TestCostOptimized_SkipModeErrorIsNotACapableProviderFault(t *testing.T) {
	p := &mockProvider{name: "first", models: []string{"gpt-4o"}}
	s := NewCostOptimized(
		[]Target{{VirtualKey: "first"}},
		newLookup(p),
		models.Catalog{"first/gpt-4o": unpricedEntry("first")},
		"skip",
	)

	_, err := s.SelectTargets(req("hi"))
	if err == nil {
		t.Fatal("expected an error in skip mode with no priced candidate")
	}
	// The message names the model, because the model is what makes it
	// unroutable: another model against the same config would route.
	if !strings.Contains(err.Error(), "gpt-4o") {
		t.Errorf("error should name the model, got %v", err)
	}
}

func TestCostOptimized_NoTargets(t *testing.T) {
	s := NewCostOptimized(nil, newLookup(), models.Catalog{})
	keys, err := s.SelectTargets(providers.Request{Model: "gpt-4o"})
	if err != nil {
		t.Fatal(err)
	}
	if keys != nil {
		t.Errorf("expected no candidates with no targets, got %v", keys)
	}
}
