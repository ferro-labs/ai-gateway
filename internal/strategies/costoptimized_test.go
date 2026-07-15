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

func TestCostOptimized_SkipsUnpricedCatalogEntryWhenPricedCandidateExists(t *testing.T) {
	unpriced := &mockProvider{name: "unpriced", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "unpriced"}}
	priced := &mockProvider{name: "priced", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "priced"}}

	catalog := models.Catalog{
		"unpriced/gpt-4o": {
			Provider: "unpriced",
			ModelID:  "gpt-4o",
			Mode:     models.ModeChat,
			Pricing:  models.Pricing{},
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
	targets := []Target{{VirtualKey: "unpriced"}, {VirtualKey: "priced"}}
	s := NewCostOptimized(targets, newLookup(unpriced, priced), catalog)

	resp, err := s.Execute(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "priced" {
		t.Errorf("expected priced provider, got %q", resp.ID)
	}
}

func TestCostOptimized_DoesNotRankOutputOnlyPricingAsFreeInput(t *testing.T) {
	outputOnly := &mockProvider{name: "output-only", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "output-only"}}
	priced := &mockProvider{name: "priced", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "priced"}}

	catalog := models.Catalog{
		"output-only/gpt-4o": {
			Provider: "output-only",
			ModelID:  "gpt-4o",
			Mode:     models.ModeChat,
			Pricing: models.Pricing{
				OutputPerMTokens: ptrF(2.0),
			},
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

	resp, err := s.Execute(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "priced" {
		t.Errorf("expected priced provider, got %q", resp.ID)
	}
}

func TestCostOptimized_FallsBackToFirstCompatibleWhenAllCandidatesUnpriced(t *testing.T) {
	first := &mockProvider{name: "first", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "first"}}
	second := &mockProvider{name: "second", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "second"}}

	catalog := models.Catalog{
		"first/gpt-4o": {
			Provider: "first",
			ModelID:  "gpt-4o",
			Mode:     models.ModeChat,
			Pricing:  models.Pricing{},
		},
		"second/gpt-4o": {
			Provider: "second",
			ModelID:  "gpt-4o",
			Mode:     models.ModeChat,
			Pricing:  models.Pricing{},
		},
	}
	targets := []Target{{VirtualKey: "first"}, {VirtualKey: "second"}}
	s := NewCostOptimized(targets, newLookup(first, second), catalog)

	resp, err := s.Execute(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "first" {
		t.Errorf("expected first compatible provider fallback, got %q", resp.ID)
	}
}

func TestCostOptimized_InvalidUnpricedStrategyFallsBackToFirstCompatible(t *testing.T) {
	first := &mockProvider{name: "first", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "first"}}
	second := &mockProvider{name: "second", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "second"}}

	catalog := models.Catalog{
		"first/gpt-4o": {
			Provider: "first",
			ModelID:  "gpt-4o",
			Mode:     models.ModeChat,
			Pricing:  models.Pricing{},
		},
		"second/gpt-4o": {
			Provider: "second",
			ModelID:  "gpt-4o",
			Mode:     models.ModeChat,
			Pricing:  models.Pricing{},
		},
	}
	targets := []Target{{VirtualKey: "first"}, {VirtualKey: "second"}}
	s := NewCostOptimized(targets, newLookup(first, second), catalog, "unknown")

	resp, err := s.Execute(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "first" {
		t.Errorf("expected invalid unpriced strategy to use fallback mode, got %q", resp.ID)
	}
}

func TestCostOptimized_AllowTreatsUnpricedAsZeroCost(t *testing.T) {
	priced := &mockProvider{name: "priced", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "priced"}}
	unpriced := &mockProvider{name: "unpriced", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "unpriced"}}

	catalog := models.Catalog{
		"priced/gpt-4o": {
			Provider: "priced",
			ModelID:  "gpt-4o",
			Mode:     models.ModeChat,
			Pricing: models.Pricing{
				InputPerMTokens: ptrF(1.0),
			},
		},
		"unpriced/gpt-4o": {
			Provider: "unpriced",
			ModelID:  "gpt-4o",
			Mode:     models.ModeChat,
			Pricing:  models.Pricing{},
		},
	}
	targets := []Target{{VirtualKey: "priced"}, {VirtualKey: "unpriced"}}
	s := NewCostOptimized(targets, newLookup(priced, unpriced), catalog, "allow")

	resp, err := s.Execute(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "unpriced" {
		t.Errorf("expected allow mode to pick zero-cost unpriced provider, got %q", resp.ID)
	}
}

func TestCostOptimized_SkipErrorsWhenAllCandidatesUnpriced(t *testing.T) {
	first := &mockProvider{name: "first", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "first"}}
	second := &mockProvider{name: "second", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "second"}}

	catalog := models.Catalog{
		"first/gpt-4o": {
			Provider: "first",
			ModelID:  "gpt-4o",
			Mode:     models.ModeChat,
			Pricing:  models.Pricing{},
		},
		"second/gpt-4o": {
			Provider: "second",
			ModelID:  "gpt-4o",
			Mode:     models.ModeChat,
			Pricing:  models.Pricing{},
		},
	}
	targets := []Target{{VirtualKey: "first"}, {VirtualKey: "second"}}
	s := NewCostOptimized(targets, newLookup(first, second), catalog, "skip")

	_, err := s.Execute(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hello"}},
	})
	if err == nil {
		t.Fatal("expected error when skip mode has no priced candidates")
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

func TestCostOptimized_UnresolvableSelectedTargetReturnsError(t *testing.T) {
	mp := &mockProvider{name: "cheap", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "cheap"}}
	s := NewCostOptimized(
		[]Target{{VirtualKey: "cheap"}},
		lookupMissingAfterFirstHit(mp),
		buildCatalog(),
	)

	_, err := s.Execute(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error when selected provider is no longer resolvable")
	}
}

func TestCostOptimized_ErrorsWhenNoProviderSupportsModel(t *testing.T) {
	p1 := &mockProvider{name: "cheap", models: []string{"gpt-3.5-turbo"}, resp: &providers.Response{ID: "wrong"}}
	p2 := &mockProvider{name: "expensive", models: []string{"claude-3"}, resp: &providers.Response{ID: "wrong"}}

	targets := []Target{{VirtualKey: "cheap"}, {VirtualKey: "expensive"}}
	s := NewCostOptimized(targets, newLookup(p1, p2), buildCatalog())

	_, err := s.Execute(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error when no provider supports model")
	}
	if err.Error() != "no provider supports model gpt-4o" {
		t.Errorf("unexpected error: %v", err)
	}
	if p1.calls != 0 || p2.calls != 0 {
		t.Errorf("providers should not be called, got cheap=%d expensive=%d", p1.calls, p2.calls)
	}
}

func TestCostOptimized_NoTargets(t *testing.T) {
	s := NewCostOptimized(nil, newLookup(), models.Catalog{})
	_, err := s.Execute(context.Background(), providers.Request{Model: "gpt-4o"})
	if err == nil {
		t.Fatal("expected error for no targets")
	}
}

// TestCostOptimized_AggregatorExcludedWhenPricedDirectProviderExists verifies
// that a provider marked as an aggregator is not selected over a direct
// provider with known catalog pricing, even if the aggregator's catalog price
// appears cheaper (a static catalog price can't represent what an aggregator
// will actually charge once it dynamically routes the request).
func TestCostOptimized_AggregatorExcludedWhenPricedDirectProviderExists(t *testing.T) {
	agg := &mockProvider{name: "agg", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "agg"}}
	direct := &mockProvider{name: "direct", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "direct"}}

	catalog := models.Catalog{
		"agg/gpt-4o": {
			Provider: "agg",
			ModelID:  "gpt-4o",
			Mode:     models.ModeChat,
			Pricing: models.Pricing{
				InputPerMTokens:  ptrF(0.01), // appears cheapest, but is a dynamically-routed aggregator
				OutputPerMTokens: ptrF(0.02),
			},
		},
		"direct/gpt-4o": {
			Provider: "direct",
			ModelID:  "gpt-4o",
			Mode:     models.ModeChat,
			Pricing: models.Pricing{
				InputPerMTokens:  ptrF(1.0),
				OutputPerMTokens: ptrF(2.0),
			},
		},
	}
	targets := []Target{{VirtualKey: "agg"}, {VirtualKey: "direct"}}
	s := NewCostOptimized(targets, newLookup(agg, direct), catalog).
		WithAggregatorPredicate(func(vk string) bool { return vk == "agg" })

	resp, err := s.Execute(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "direct" {
		t.Errorf("expected aggregator to be excluded in favour of direct provider, got %q", resp.ID)
	}
}

// TestCostOptimized_AggregatorUsedAsFallbackWhenOnlyProvider verifies that an
// aggregator IS used (as fallback) when it is the only available provider for
// the requested model — the default unpricedStrategy is "fallback".
func TestCostOptimized_AggregatorUsedAsFallbackWhenOnlyProvider(t *testing.T) {
	agg := &mockProvider{name: "agg", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "agg"}}

	catalog := models.Catalog{
		"agg/gpt-4o": {
			Provider: "agg",
			ModelID:  "gpt-4o",
			Mode:     models.ModeChat,
			Pricing: models.Pricing{
				InputPerMTokens:  ptrF(0.01),
				OutputPerMTokens: ptrF(0.02),
			},
		},
	}
	targets := []Target{{VirtualKey: "agg"}}
	s := NewCostOptimized(targets, newLookup(agg), catalog).
		WithAggregatorPredicate(func(vk string) bool { return vk == "agg" })

	resp, err := s.Execute(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "agg" {
		t.Errorf("expected aggregator to be used as fallback when only provider available, got %q", resp.ID)
	}
}

// TestCostOptimized_AggregatorExcludedUnderAllowMode ensures that
// unpricedStrategy=allow does not let a *priced* aggregator win cost ranking
// just because its catalog entry looks cheapest. The aggregator here has a
// real (low) catalog price, not an absent one — allow mode only changes how
// unpriced candidates are treated, so a catalog with no agg/direct pricing
// (as buildCatalog() has) would leave both candidates unpriced and never
// exercise this path at all.
func TestCostOptimized_AggregatorExcludedUnderAllowMode(t *testing.T) {
	agg := &mockProvider{name: "agg", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "agg"}}
	direct := &mockProvider{name: "direct", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "direct"}}

	catalog := models.Catalog{
		"agg/gpt-4o": {
			Provider: "agg",
			ModelID:  "gpt-4o",
			Mode:     models.ModeChat,
			Pricing: models.Pricing{
				InputPerMTokens:  ptrF(0.01), // priced, and cheapest on paper
				OutputPerMTokens: ptrF(0.02),
			},
		},
		"direct/gpt-4o": {
			Provider: "direct",
			ModelID:  "gpt-4o",
			Mode:     models.ModeChat,
			Pricing: models.Pricing{
				InputPerMTokens:  ptrF(1.0),
				OutputPerMTokens: ptrF(2.0),
			},
		},
	}
	targets := []Target{{VirtualKey: "agg"}, {VirtualKey: "direct"}}
	s := NewCostOptimized(targets, newLookup(agg, direct), catalog, "allow").
		WithAggregatorPredicate(func(vk string) bool { return vk == "agg" })

	resp, err := s.Execute(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// agg must NOT win even though it is priced cheapest and allow mode ranks
	// unpriced candidates too — isAggregator excludes it regardless of mode.
	if resp.ID == "agg" {
		t.Errorf("allow mode: aggregator won cost ranking despite isAggregator=true; expected direct provider")
	}
}

// TestCostOptimized_AggregatorPositionalFallbackSkippedWhenDirectExists ensures
// the candidates[0] positional fallback does not select an aggregator when a
// non-aggregator direct provider is also available.
func TestCostOptimized_AggregatorPositionalFallbackSkippedWhenDirectExists(t *testing.T) {
	agg := &mockProvider{name: "agg", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "agg"}}
	// direct has no catalog entry (isModel=false) so it normally falls to positional fallback.
	direct := &mockProvider{name: "direct-unpriced", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "direct-unpriced"}}

	catalog := buildCatalog() // only "agg" in catalog; direct-unpriced has no entry
	targets := []Target{{VirtualKey: "agg"}, {VirtualKey: "direct-unpriced"}}
	s := NewCostOptimized(targets, newLookup(agg, direct), catalog).
		WithAggregatorPredicate(func(vk string) bool { return vk == "agg" })

	resp, err := s.Execute(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// agg is listed first but must not win the positional fallback when a direct
	// provider exists.
	if resp.ID == "agg" {
		t.Errorf("positional fallback selected aggregator even though direct-unpriced was available")
	}
}

func TestCostOptimized_NilAggregatorPredicateDoesNotPanic(t *testing.T) {
	cheap := &mockProvider{name: "cheap", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "cheap"}}
	expensive := &mockProvider{name: "expensive", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "expensive"}}

	catalog := buildCatalog()
	targets := []Target{{VirtualKey: "cheap"}, {VirtualKey: "expensive"}}
	// isAggregator stays nil — no WithAggregatorPredicate call.
	s := NewCostOptimized(targets, newLookup(cheap, expensive), catalog)

	resp, err := s.Execute(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "cheap" {
		t.Errorf("nil isAggregator: expected normal cost routing to pick cheap, got %q", resp.ID)
	}
}
