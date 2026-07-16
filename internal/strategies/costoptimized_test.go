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

// aggregatorCatalogAndTargets builds a catalog where the aggregator's
// catalog-listed price looks cheapest, plus a non-aggregator candidate that
// is more expensive, so tests can assert the aggregator never wins on price.
func aggregatorCatalogAndTargets() (models.Catalog, providers.Provider, providers.Provider) {
	aggregator := &mockProvider{name: "aggregator", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "aggregator"}}
	normal := &mockProvider{name: "normal", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "normal"}}
	catalog := models.Catalog{
		"aggregator/gpt-4o": {
			Provider: "aggregator",
			ModelID:  "gpt-4o",
			Mode:     models.ModeChat,
			Pricing: models.Pricing{
				InputPerMTokens:  ptrF(0.01), // looks cheapest
				OutputPerMTokens: ptrF(0.01),
			},
		},
		"normal/gpt-4o": {
			Provider: "normal",
			ModelID:  "gpt-4o",
			Mode:     models.ModeChat,
			Pricing: models.Pricing{
				InputPerMTokens:  ptrF(5.0),
				OutputPerMTokens: ptrF(5.0),
			},
		},
	}
	return catalog, aggregator, normal
}

func aggregatorPredicate(vk string) bool { return vk == "aggregator" }

func TestCostOptimized_AggregatorExcludedFromRankingEvenWhenCheapest(t *testing.T) {
	for _, mode := range []string{"fallback", "skip", "allow"} {
		t.Run(mode, func(t *testing.T) {
			catalog, aggregator, normal := aggregatorCatalogAndTargets()
			targets := []Target{{VirtualKey: "aggregator"}, {VirtualKey: "normal"}}
			s := NewCostOptimized(targets, newLookup(aggregator, normal), catalog, mode).
				WithAggregatorPredicate(aggregatorPredicate)

			resp, err := s.Execute(context.Background(), providers.Request{
				Model:    "gpt-4o",
				Messages: []providers.Message{{Role: "user", Content: "hello world"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if resp.ID != "normal" {
				t.Errorf("mode %s: expected non-aggregator to win despite aggregator looking cheaper, got %q", mode, resp.ID)
			}
		})
	}
}

func TestCostOptimized_AggregatorUsedAsLastResortWhenOnlyCandidate(t *testing.T) {
	for _, mode := range []string{"fallback", "skip", "allow"} {
		t.Run(mode, func(t *testing.T) {
			catalog, aggregator, _ := aggregatorCatalogAndTargets()
			targets := []Target{{VirtualKey: "aggregator"}}
			s := NewCostOptimized(targets, newLookup(aggregator), catalog, mode).
				WithAggregatorPredicate(aggregatorPredicate)

			resp, err := s.Execute(context.Background(), providers.Request{
				Model:    "gpt-4o",
				Messages: []providers.Message{{Role: "user", Content: "hello world"}},
			})
			if mode == "skip" {
				// skip mode requires a priced candidate, but the aggregator
				// should still be usable as a last resort since it's the
				// only candidate at all.
				if err != nil {
					t.Fatalf("expected aggregator last-resort fallback in skip mode, got error: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if resp.ID != "aggregator" {
				t.Errorf("mode %s: expected aggregator as last-resort fallback, got %q", mode, resp.ID)
			}
		})
	}
}

// TestCostOptimized_FallbackPrefersNonAggregatorEvenWhenListedFirst covers the
// positional-fallback path (no priced candidate at all): an aggregator listed
// FIRST in targets must not win merely because of config order — a
// non-aggregator candidate later in the list must be preferred, with the
// aggregator used only when no non-aggregator exists at all (see
// TestCostOptimized_AggregatorUsedAsLastResortWhenOnlyCandidate).
func TestCostOptimized_FallbackPrefersNonAggregatorEvenWhenListedFirst(t *testing.T) {
	aggregator := &mockProvider{name: "aggregator", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "aggregator"}}
	normal := &mockProvider{name: "normal", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "normal"}}

	// Empty catalog: neither candidate is priced, so both "fallback" and
	// "allow" modes fall through to positional selection.
	catalog := models.Catalog{}
	for _, mode := range []string{"fallback", "allow"} {
		t.Run(mode, func(t *testing.T) {
			targets := []Target{{VirtualKey: "aggregator"}, {VirtualKey: "normal"}}
			s := NewCostOptimized(targets, newLookup(aggregator, normal), catalog, mode).
				WithAggregatorPredicate(aggregatorPredicate)

			resp, err := s.Execute(context.Background(), providers.Request{
				Model:    "gpt-4o",
				Messages: []providers.Message{{Role: "user", Content: "hello world"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if resp.ID != "normal" {
				t.Errorf("mode %s: expected non-aggregator fallback despite aggregator being listed first, got %q", mode, resp.ID)
			}
		})
	}
}

// mockStreamProvider is mockProvider plus CompleteStream, for SelectTargets
// tests (which only consider providers.StreamProvider candidates).
type mockStreamProvider struct {
	mockProvider
}

func (m *mockStreamProvider) CompleteStream(_ context.Context, _ providers.Request) (<-chan providers.StreamChunk, error) {
	ch := make(chan providers.StreamChunk)
	close(ch)
	return ch, nil
}

// TestCostOptimized_SelectTargetsOrdersNonAggregatorsBeforeAggregators covers
// SelectTargets' skip-mode last-resort path: with no priced candidate at all,
// an aggregator listed FIRST in targets must not be ordered ahead of a
// non-aggregator candidate merely because of config order.
func TestCostOptimized_SelectTargetsOrdersNonAggregatorsBeforeAggregators(t *testing.T) {
	aggregator := &mockStreamProvider{mockProvider{name: "aggregator", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "aggregator"}}}
	normal := &mockStreamProvider{mockProvider{name: "normal", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "normal"}}}

	// Catalog entries exist (modelFound=true) but carry no pricing
	// (hasPrice=false), so skip mode has no priced candidate and falls
	// through to the last-resort ordering path being tested.
	catalog := models.Catalog{
		"aggregator/gpt-4o": {Provider: "aggregator", ModelID: "gpt-4o", Mode: models.ModeChat, Pricing: models.Pricing{}},
		"normal/gpt-4o":     {Provider: "normal", ModelID: "gpt-4o", Mode: models.ModeChat, Pricing: models.Pricing{}},
	}
	targets := []Target{{VirtualKey: "aggregator"}, {VirtualKey: "normal"}}
	s := NewCostOptimized(targets, newLookup(aggregator, normal), catalog, "skip").
		WithAggregatorPredicate(aggregatorPredicate)

	keys, err := s.SelectTargets(providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hello world"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) < 2 {
		t.Fatalf("expected at least 2 ordered keys, got %v", keys)
	}
	if keys[0] != "normal" {
		t.Errorf("expected non-aggregator target ordered first despite aggregator being listed first in config, got order %v", keys)
	}
}

func TestCostOptimized_NonAggregatorCandidateUnaffectedByAggregatorPredicate(t *testing.T) {
	cheap := &mockProvider{name: "cheap", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "cheap"}}
	expensive := &mockProvider{name: "expensive", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "expensive"}}

	catalog := buildCatalog()
	targets := []Target{{VirtualKey: "cheap"}, {VirtualKey: "expensive"}}
	// Predicate never matches, so neither candidate is treated as an
	// aggregator — behavior should be identical to the non-aggregator-aware
	// baseline test (TestCostOptimized_PicksCheapest).
	s := NewCostOptimized(targets, newLookup(cheap, expensive), catalog).
		WithAggregatorPredicate(func(string) bool { return false })

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

// aliasedProvider wraps mockProvider-like behavior but reports a canonical
// Name() distinct from the routing alias it's registered under — simulating
// a multi-instance provider registered under two different aliases that both
// resolve to the same underlying provider type (e.g. two Ollama Cloud
// accounts both reporting Name() == "ollama-cloud").
type aliasedProvider struct {
	alias         string
	canonicalName string
	models        []string
	resp          *providers.Response
}

func (a *aliasedProvider) Name() string                  { return a.canonicalName }
func (a *aliasedProvider) SupportedModels() []string     { return a.models }
func (a *aliasedProvider) Models() []providers.ModelInfo { return nil }
func (a *aliasedProvider) SupportsModel(model string) bool {
	for _, m := range a.models {
		if m == model {
			return true
		}
	}
	return false
}
func (a *aliasedProvider) Complete(_ context.Context, _ providers.Request) (*providers.Response, error) {
	return a.resp, nil
}

// TestCostOptimized_CatalogLookupUsesCanonicalProviderName confirms the
// catalog lookup key is built from the provider's Name() (canonical type),
// not the routing alias (VirtualKey) — the multi-instance scenario where two
// targets with different aliases both resolve to a provider reporting the
// same canonical Name(), and both must price identically.
func TestCostOptimized_CatalogLookupUsesCanonicalProviderName(t *testing.T) {
	instanceA := &aliasedProvider{alias: "ollama-cloud-a", canonicalName: "ollama-cloud", models: []string{"llama3"}, resp: &providers.Response{ID: "a"}}
	instanceB := &aliasedProvider{alias: "ollama-cloud-b", canonicalName: "ollama-cloud", models: []string{"llama3"}, resp: &providers.Response{ID: "b"}}

	// Catalog is keyed by canonical provider type only — there is no entry
	// for either alias. If the strategy looked up by alias, neither instance
	// would resolve pricing.
	catalog := models.Catalog{
		"ollama-cloud/llama3": {
			Provider: "ollama-cloud",
			ModelID:  "llama3",
			Mode:     models.ModeChat,
			Pricing: models.Pricing{
				InputPerMTokens:  ptrF(2.0),
				OutputPerMTokens: ptrF(4.0),
			},
		},
	}

	lookupByAlias := func(alias string) (providers.Provider, bool) {
		switch alias {
		case "ollama-cloud-a":
			return instanceA, true
		case "ollama-cloud-b":
			return instanceB, true
		default:
			return nil, false
		}
	}

	req := providers.Request{
		Model:    "llama3",
		Messages: []providers.Message{{Role: "user", Content: "hello world"}},
	}

	sA := NewCostOptimized([]Target{{VirtualKey: "ollama-cloud-a"}}, lookupByAlias, catalog)
	respA, err := sA.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("instance A: unexpected error: %v", err)
	}
	if respA.ID != "a" {
		t.Errorf("instance A: expected dispatch to alias a, got %q", respA.ID)
	}

	sB := NewCostOptimized([]Target{{VirtualKey: "ollama-cloud-b"}}, lookupByAlias, catalog)
	respB, err := sB.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("instance B: unexpected error: %v", err)
	}
	if respB.ID != "b" {
		t.Errorf("instance B: expected dispatch to alias b, got %q", respB.ID)
	}

	// Both should resolve the same catalog-derived cost since both alias to
	// the same canonical provider type.
	sBoth := NewCostOptimized([]Target{{VirtualKey: "ollama-cloud-a"}, {VirtualKey: "ollama-cloud-b"}}, lookupByAlias, catalog, "skip")
	// skip mode requires a priced candidate; both alias to a priced canonical
	// entry, so this must not error.
	if _, err := sBoth.Execute(context.Background(), req); err != nil {
		t.Errorf("expected both aliases to resolve catalog pricing via canonical name, got error: %v", err)
	}
}
