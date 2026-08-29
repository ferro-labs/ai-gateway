package aigateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/pkg/logger"
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
	// The target names the provider registered below: AllModels lists what the
	// targets allowlist admits, so a provider no target names contributes
	// nothing to it.
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "anthropic"}},
	})

	if err != nil {
		t.Fatalf("New: %v", err)
	}
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
// catalog set exactly, regardless of the hardcoded ConfiguredModels() slice.
//
// The mode is fallback because the subject is the catalog, not the strategy:
// only a mode that walks past its first target reaches all four, and the
// listing publishes what routing would reach (see routingServes).
func TestAllModels_MatchesCatalogForRegisteredProviders(t *testing.T) {
	catalogued := []string{"anthropic", "xai", "gemini", "groq"}
	targets := make([]config.Target, len(catalogued))
	for i, name := range catalogued {
		targets[i] = config.Target{VirtualKey: name}
	}
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeFallback},
		Targets:  targets,
	})

	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, name := range catalogued {
		gw.RegisterProvider(&mockProvider{name: name, models: []string{name + "-stale"}})
	}

	all := gw.AllModels()
	for _, name := range catalogued {
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
	const name = "no-such-catalog-provider-xyz"
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: name}},
	})

	if err != nil {
		t.Fatalf("New: %v", err)
	}
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
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "unused"}},
	})

	if err != nil {
		t.Fatalf("New: %v", err)
	}
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
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "unused"}},
	})

	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.RegisterProvider(&mockProvider{name: "anthropic", models: []string{"claude-hardcoded-only"}})

	if _, ok := gw.FindByModel("definitely-not-a-real-model-zzz"); ok {
		t.Fatal("FindByModel accepted an unknown model")
	}
}

func TestNewWithContextStopsConstructionWhenCatalogFetchIsCanceled(t *testing.T) {
	requestStarted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	t.Setenv(models.CatalogURLEnv, server.URL)
	t.Setenv(models.CatalogFetchTimeoutEnv, time.Minute.String())

	ctx, cancel := context.WithCancel(t.Context())
	type result struct {
		gw  *Gateway
		err error
	}
	done := make(chan result, 1)
	go func() {
		gw, err := New(config.Config{
			Strategy: config.StrategyConfig{Mode: config.ModeSingle},
			Targets:  []config.Target{{VirtualKey: "openai"}},
		}, WithContext(ctx))
		done <- result{gw: gw, err: err}
	}()

	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("catalog request did not start")
	}
	cancel()
	select {
	case got := <-done:
		if got.gw != nil {
			_ = got.gw.Close()
			t.Fatal("New returned a gateway after construction was canceled")
		}
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("New error = %v, want context.Canceled", got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("New did not return after context cancellation")
	}
}

// TestGateway_RefreshCatalog_AbortsOnCanceledContext proves refreshCatalog
// passes its context to the in-flight remote request.
func TestGateway_RefreshCatalog_AbortsOnCanceledContext(t *testing.T) {
	requestStarted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	t.Setenv(models.CatalogURLEnv, server.URL)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	g := &Gateway{log: logger.Default()}
	done := make(chan struct{})
	go func() {
		defer close(done)
		g.refreshCatalog(ctx)
	}()

	// Both waits are unbounded on purpose. The assertion is that refreshCatalog
	// eventually returns, not that it returns inside some wall-clock budget, and
	// a deadline short enough to be useful is a deadline a loaded -race runner
	// will trip on its own. If either signal never arrives, go test's own
	// timeout fails the run with a goroutine dump naming the line it is blocked
	// on — which says more than "did not return" ever did.
	<-requestStarted // the handler closes it, so the fetch is in flight
	cancel()
	<-done // closed when refreshCatalog returns
}
