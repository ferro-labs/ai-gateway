package aigateway

import (
	"sort"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// catalogFallbackProvider is a mockProvider whose Models() returns a real
// ModelInfo slice, so the hardcoded-fallback branch of AllModels (used when the
// catalog has no entries for the provider) can be exercised.
type catalogFallbackProvider struct {
	mockProvider
}

func (p *catalogFallbackProvider) Models() []core.ModelInfo {
	return core.ModelsFromList(p.name, p.models)
}

// modelsOwnedBy returns the sorted model IDs in ms owned by provider name.
func modelsOwnedBy(ms []providers.ModelInfo, name string) []string {
	var out []string
	for _, m := range ms {
		if m.OwnedBy == name {
			out = append(out, m.ID)
		}
	}
	sort.Strings(out)
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestAllModels_DerivesFromCatalog asserts /v1/models output for a provider
// with catalog entries reflects the catalog, not the (stale) hardcoded slice.
// Regression guard for issue #146.
func TestAllModels_DerivesFromCatalog(t *testing.T) {
	gw, err := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "unused"}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = gw.Close() })
	// Intentionally stale hardcoded list — must NOT drive /v1/models.
	gw.RegisterProvider(&mockProvider{name: "anthropic", models: []string{"claude-stale-only"}})

	want := gw.Catalog().ModelsForProvider("anthropic")
	if len(want) == 0 {
		t.Fatal("precondition: catalog has no anthropic models")
	}

	got := modelsOwnedBy(gw.AllModels(), "anthropic")
	if !equalStrings(got, want) {
		t.Fatalf("AllModels anthropic = %d models, want catalog set of %d", len(got), len(want))
	}
	for _, id := range got {
		if id == "claude-stale-only" {
			t.Fatal("stale hardcoded model leaked into /v1/models")
		}
	}
}

// TestAllModels_MatchesCatalogForRegisteredProviders is the drift guard: for
// every provider that has catalog entries, the exposed model set must equal the
// catalog set exactly, regardless of the hardcoded SupportedModels() slice.
func TestAllModels_MatchesCatalogForRegisteredProviders(t *testing.T) {
	gw, err := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "unused"}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = gw.Close() })
	for _, name := range []string{"anthropic", "xai", "gemini", "groq"} {
		gw.RegisterProvider(&mockProvider{name: name, models: []string{name + "-stale"}})
	}

	all := gw.AllModels()
	for _, name := range []string{"anthropic", "xai", "gemini", "groq"} {
		want := gw.Catalog().ModelsForProvider(name)
		got := modelsOwnedBy(all, name)
		if !equalStrings(got, want) {
			t.Errorf("provider %s: exposed %d models, catalog has %d (drift)", name, len(got), len(want))
		}
	}
}

// TestAllModels_FallsBackToHardcodedWhenCatalogEmpty asserts a provider with no
// catalog entries still exposes its hardcoded Models() (e.g. self-hosted Ollama).
func TestAllModels_FallsBackToHardcodedWhenCatalogEmpty(t *testing.T) {
	gw, err := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "unused"}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = gw.Close() })
	const name = "no-such-catalog-provider-xyz"
	if len(gw.Catalog().ModelsForProvider(name)) != 0 {
		t.Fatalf("precondition: %q unexpectedly present in catalog", name)
	}
	p := &catalogFallbackProvider{mockProvider{name: name, models: []string{"local-a", "local-b"}}}
	gw.RegisterProvider(p)

	got := modelsOwnedBy(gw.AllModels(), name)
	if !equalStrings(got, []string{"local-a", "local-b"}) {
		t.Fatalf("fallback models = %v, want [local-a local-b]", got)
	}
}

// TestRouting_AcceptsCatalogModelNotInHardcodedSlice proves the routing index
// now accepts valid catalog models an exact-match provider's slice omits.
func TestRouting_AcceptsCatalogModelNotInHardcodedSlice(t *testing.T) {
	gw, err := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "unused"}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = gw.Close() })
	gw.RegisterProvider(&mockProvider{name: "anthropic", models: []string{"claude-hardcoded-only"}})

	catModels := gw.Catalog().ModelsForProvider("anthropic")
	if len(catModels) == 0 {
		t.Fatal("precondition: no catalog anthropic models")
	}
	// Pick a catalog model that is NOT in the hardcoded slice.
	var target string
	for _, m := range catModels {
		if m != "claude-hardcoded-only" {
			target = m
			break
		}
	}
	if target == "" {
		t.Fatal("could not find a catalog-only model")
	}

	p, ok := gw.FindByModel(target)
	if !ok {
		t.Fatalf("FindByModel(%q) = not found, want anthropic via catalog routing", target)
	}
	if p.Name() != "anthropic" {
		t.Fatalf("FindByModel(%q) routed to %q, want anthropic", target, p.Name())
	}
}

// TestRouting_RejectsUnknownModel ensures the catalog fallback does not make
// routing accept genuinely unknown models.
func TestRouting_RejectsUnknownModel(t *testing.T) {
	gw, err := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "unused"}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = gw.Close() })
	gw.RegisterProvider(&mockProvider{name: "anthropic", models: []string{"claude-hardcoded-only"}})

	if _, ok := gw.FindByModel("definitely-not-a-real-model-zzz"); ok {
		t.Fatal("FindByModel accepted an unknown model")
	}
}
