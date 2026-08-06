package aigateway

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/internal/requestlog"
	"github.com/ferro-labs/ai-gateway/plugin"
	pluginlogger "github.com/ferro-labs/ai-gateway/plugin/logger"
	"github.com/ferro-labs/ai-gateway/plugin/ratelimit"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// narrowSupportProvider advertises more models than its own SupportsModel
// admits — the shape of a real provider whose SupportsModel is a hardcoded
// prefix list while the model set the gateway indexes and serves at
// /v1/models is far wider.
type narrowSupportProvider struct {
	mockStreamProvider
	supported []string
}

func (p *narrowSupportProvider) SupportsModel(model string) bool {
	return slices.Contains(p.supported, model)
}

// TestWithIndexedModels_MirrorsStreamingCapability asserts the widened view
// claims StreamProvider exactly when the provider beneath it does. The
// concurrency and circuit-breaker decorators compose over this view and assert
// that interface, and the strategies' streaming target ranking asserts it on
// the composed result — a view that forged it would make a non-streaming
// provider look streamable, and one that dropped it would hide a real stream.
func TestWithIndexedModels_MirrorsStreamingCapability(t *testing.T) {
	index := map[string][]string{"indexed-model": {"p"}}

	tests := []struct {
		name       string
		provider   providers.Provider
		wantStream bool
	}{
		{name: "non-streaming", provider: &mockProvider{name: "p"}, wantStream: false},
		{name: "streaming", provider: &mockStreamProvider{mockProvider: mockProvider{name: "p"}}, wantStream: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			view := withIndexedModels("p", tt.provider, index)
			if _, ok := view.(providers.StreamProvider); ok != tt.wantStream {
				t.Errorf("view implements StreamProvider = %v, want %v", ok, tt.wantStream)
			}
			if !view.SupportsModel("indexed-model") {
				t.Error("SupportsModel(\"indexed-model\") = false, want true")
			}
			if view.SupportsModel("unindexed-model") {
				t.Error("SupportsModel(\"unindexed-model\") = true, want false")
			}
		})
	}
}

// aliasTestGateway registers two strict-SupportsModel providers behind a
// fallback strategy, with "fast" aliased to a model only the first one serves.
// completeFn records the model each provider is actually asked for.
func aliasTestGateway(t *testing.T, served map[string]string) *Gateway {
	t.Helper()
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeFallback},
		Targets:  []config.Target{{VirtualKey: "openai"}, {VirtualKey: "anthropic"}},
		Aliases:  map[string]string{"fast": "gpt-4o-mini"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	record := func(name string) func(context.Context, providers.Request) (*providers.Response, error) {
		return func(_ context.Context, req providers.Request) (*providers.Response, error) {
			served[name] = req.Model
			return &providers.Response{ID: name, Model: req.Model}, nil
		}
	}
	gw.RegisterProvider(&mockStreamProvider{mockProvider: mockProvider{
		name: "openai", models: []string{"gpt-4o-mini"}, completeFn: record("openai"),
	}})
	gw.RegisterProvider(&mockStreamProvider{mockProvider: mockProvider{
		name: "anthropic", models: []string{"claude-sonnet-4"}, completeFn: record("anthropic"),
	}})
	return gw
}

// TestGateway_FindByModel_ResolvesAlias asserts the lookups that admit a
// request accept the same model names Route does. The HTTP chat handler
// pre-checks the raw client model with these before calling Route, so an alias
// they reject is a 400 for a request Route would have served.
func TestGateway_FindByModel_ResolvesAlias(t *testing.T) {
	tests := []struct {
		name      string
		model     string
		wantFound bool
		wantOwner string
	}{
		{name: "alias", model: "fast", wantFound: true, wantOwner: "openai"},
		{name: "alias target", model: "gpt-4o-mini", wantFound: true, wantOwner: "openai"},
		{name: "other provider model", model: "claude-sonnet-4", wantFound: true, wantOwner: "anthropic"},
		{name: "unknown", model: "no-such-model", wantFound: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gw := aliasTestGateway(t, map[string]string{})

			p, ok := gw.FindByModel(tt.model)
			if ok != tt.wantFound {
				t.Fatalf("FindByModel(%q) found = %v, want %v", tt.model, ok, tt.wantFound)
			}
			if !tt.wantFound {
				return
			}
			if p.Name() != tt.wantOwner {
				t.Errorf("FindByModel(%q) = %q, want %q", tt.model, p.Name(), tt.wantOwner)
			}

			sp, ok := gw.FindStreamingByModel(tt.model)
			if !ok {
				t.Fatalf("FindStreamingByModel(%q) found = false, want true", tt.model)
			}
			if sp.Name() != tt.wantOwner {
				t.Errorf("FindStreamingByModel(%q) = %q, want %q", tt.model, sp.Name(), tt.wantOwner)
			}
		})
	}
}

// TestGateway_Route_AliasAdmittedAndResolved walks the pre-check and the route
// together: the alias is admitted, and the provider is called with the target
// model rather than the alias.
func TestGateway_Route_AliasAdmittedAndResolved(t *testing.T) {
	served := map[string]string{}
	gw := aliasTestGateway(t, served)

	if _, ok := gw.FindByModel("fast"); !ok {
		t.Fatal("FindByModel(\"fast\") found = false, want true")
	}

	resp, err := gw.Route(context.Background(), providers.Request{
		Model:    "fast",
		Messages: []providers.Message{{Role: roleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if resp.Provider != "openai" {
		t.Errorf("Route provider = %q, want openai", resp.Provider)
	}
	if served["openai"] != "gpt-4o-mini" {
		t.Errorf("provider received model %q, want gpt-4o-mini", served["openai"])
	}
}

// indexedModelGateway registers a provider that indexes indexedModel but whose
// own SupportsModel refuses it, behind a fallback strategy.
func indexedModelGateway(t *testing.T, cfg config.Config, indexedModel string) (*Gateway, *narrowSupportProvider) {
	t.Helper()
	gw, err := newTestGateway(t, cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p := &narrowSupportProvider{
		mockStreamProvider: mockStreamProvider{mockProvider: mockProvider{
			name:   mockProviderName,
			models: []string{"mock-tiny", indexedModel},
			resp:   &providers.Response{ID: "1", Model: indexedModel},
		}},
		supported: []string{"mock-tiny"},
	}
	gw.RegisterProvider(p)
	if p.SupportsModel(indexedModel) {
		t.Fatalf("provider SupportsModel(%q) = true, want false — premise of this test", indexedModel)
	}
	return gw, p
}

// TestGateway_Route_IndexedModelNotInSupportsModel asserts non-streaming chat
// routes every model the gateway indexes — the set /v1/models advertises and
// FindByModel admits — and not merely the narrower set a provider's own
// SupportsModel returns. The streaming and embeddings/images surfaces already
// resolve through the index as a last resort; chat must not be the one surface
// that answers 500 for a model the gateway told the caller to ask for.
func TestGateway_Route_IndexedModelNotInSupportsModel(t *testing.T) {
	const indexedModel = "mock-catalog-only"

	cfg := config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeFallback},
		Targets:  []config.Target{{VirtualKey: mockProviderName}},
	}

	t.Run("find admits it", func(t *testing.T) {
		gw, _ := indexedModelGateway(t, cfg, indexedModel)
		if _, ok := gw.FindByModel(indexedModel); !ok {
			t.Fatalf("FindByModel(%q) found = false, want true", indexedModel)
		}
	})

	t.Run("route serves it", func(t *testing.T) {
		gw, _ := indexedModelGateway(t, cfg, indexedModel)
		resp, err := gw.Route(context.Background(), providers.Request{
			Model:    indexedModel,
			Messages: []providers.Message{{Role: roleUser, Content: "hi"}},
		})
		if err != nil {
			t.Fatalf("Route: %v", err)
		}
		if resp.Provider != mockProviderName {
			t.Errorf("Route provider = %q, want %q", resp.Provider, mockProviderName)
		}
	})

	t.Run("route stream serves it", func(t *testing.T) {
		gw, _ := indexedModelGateway(t, cfg, indexedModel)
		ch, err := gw.RouteStream(context.Background(), providers.Request{
			Model:    indexedModel,
			Messages: []providers.Message{{Role: roleUser, Content: "hi"}},
		})
		if err != nil {
			t.Fatalf("RouteStream: %v", err)
		}
		drainStream(t, ch)
	})
}

// TestGateway_Route_ReindexRebuildsStrategy asserts a model that enters the
// index after the strategy was built is routable. The strategy selects targets
// against the index it was built with, so every path that reindexes — provider
// registration, catalog refresh, live discovery — has to drop it.
func TestGateway_Route_ReindexRebuildsStrategy(t *testing.T) {
	gw, _ := indexedModelGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeFallback},
		Targets:  []config.Target{{VirtualKey: mockProviderName}},
	}, "mock-catalog-only")

	// Build and cache the strategy against the current index.
	if _, err := gw.getStrategy(); err != nil {
		t.Fatalf("getStrategy: %v", err)
	}

	const discovered = "mock-discovered"
	gw.mu.Lock()
	gw.discoveredModels[mockProviderName] = []providers.ModelInfo{{ID: discovered}}
	gw.rebuildModelIndexesLocked()
	gw.mu.Unlock()

	if _, err := gw.Route(context.Background(), providers.Request{
		Model:    discovered,
		Messages: []providers.Message{{Role: roleUser, Content: "hi"}},
	}); err != nil {
		t.Fatalf("Route: %v", err)
	}
}

// TestGateway_Route_AliasToIndexedModel composes both resolutions: an alias
// pointing at a model only the routing index knows about must be admitted and
// routed, with the provider called for the alias target.
func TestGateway_Route_AliasToIndexedModel(t *testing.T) {
	const indexedModel = "mock-catalog-only"

	gw, p := indexedModelGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeFallback},
		Targets:  []config.Target{{VirtualKey: mockProviderName}},
		Aliases:  map[string]string{"fast": indexedModel},
	}, indexedModel)

	var got string
	p.completeFn = func(_ context.Context, req providers.Request) (*providers.Response, error) {
		got = req.Model
		return &providers.Response{ID: "1", Model: req.Model}, nil
	}

	if _, ok := gw.FindByModel("fast"); !ok {
		t.Fatal("FindByModel(\"fast\") found = false, want true")
	}
	if _, err := gw.Route(context.Background(), providers.Request{
		Model:    "fast",
		Messages: []providers.Message{{Role: roleUser, Content: "hi"}},
	}); err != nil {
		t.Fatalf("Route: %v", err)
	}
	if got != indexedModel {
		t.Errorf("provider received model %q, want %q", got, indexedModel)
	}
}

// claimsEverythingProvider is the shape seven of the nine credentialed
// providers shipped: SupportsModel answers true for every model id, and the
// provider owns nothing in the routing index. It declares no wildcard, so it
// must never be offered a model — that answer is advisory and buys no traffic.
type claimsEverythingProvider struct {
	mockStreamProvider
	mu     sync.Mutex
	called []string
}

func (p *claimsEverythingProvider) SupportsModel(string) bool { return true }

func (p *claimsEverythingProvider) Complete(_ context.Context, req providers.Request) (*providers.Response, error) {
	p.mu.Lock()
	p.called = append(p.called, req.Model)
	p.mu.Unlock()
	return &providers.Response{ID: p.name, Model: req.Model, Provider: p.name}, nil
}

func (p *claimsEverythingProvider) seen() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.called)
}

// declaredWildcardProvider is claimsEverythingProvider that also declares
// core.AnyModelProvider — the deliberate opt-in an operator-named model set
// (ollama, databricks, azure-foundry) needs.
type declaredWildcardProvider struct {
	claimsEverythingProvider
}

func (p *declaredWildcardProvider) ServesAnyModel() {}

var _ core.AnyModelProvider = (*declaredWildcardProvider)(nil)

func claimsEverything(name string) *claimsEverythingProvider {
	return &claimsEverythingProvider{mockStreamProvider: mockStreamProvider{
		mockProvider: mockProvider{name: name},
	}}
}

func ownerOf(name string, models ...string) *claimsEverythingProvider {
	p := claimsEverything(name)
	p.models = models
	return p
}

// fallbackGateway wires targets in the order given, under fallback so the walk
// would visit every candidate if candidacy were not decided correctly.
func fallbackGateway(t *testing.T, targetKeys ...string) *Gateway {
	t.Helper()
	targets := make([]config.Target, len(targetKeys))
	for i, k := range targetKeys {
		targets[i] = config.Target{VirtualKey: k}
	}
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeFallback},
		Targets:  targets,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return gw
}

func chat(gw *Gateway, model string) (*providers.Response, error) {
	return gw.Route(context.Background(), providers.Request{
		Model:    model,
		Messages: []providers.Message{{Role: roleUser, Content: "canary"}},
	})
}

// TestRouting_UnownedModelReachesNoProvider is F21. Three providers answering
// SupportsModel with `return true` made candidates of themselves for a model
// name nothing owns, so the prompt body was transmitted to all three before the
// caller got its 404. Candidacy now comes from the routing index alone, so the
// request must fail having called nobody.
func TestRouting_UnownedModelReachesNoProvider(t *testing.T) {
	gw := fallbackGateway(t, "groq", "qwen", "perplexity", "openai")
	liars := []*claimsEverythingProvider{
		claimsEverything("groq"), claimsEverything("qwen"), claimsEverything("perplexity"),
	}
	for _, p := range liars {
		gw.RegisterProvider(p)
	}
	gw.RegisterProvider(ownerOf("openai", "gpt-4o-mini"))

	_, err := chat(gw, "totally-not-a-real-model")
	if !errors.Is(err, core.ErrNoCapableProvider) {
		t.Fatalf("Route error = %v, want ErrNoCapableProvider", err)
	}
	for _, p := range liars {
		if seen := p.seen(); len(seen) != 0 {
			t.Errorf("%s received the prompt for %v, want no call at all", p.name, seen)
		}
	}
}

// TestRouting_OwnershipBeatsDeclarationOrder is the second half of F21: with
// targets [groq, openai] a gpt-4o-mini request hit groq first and only reached
// openai after groq's 404, because the target list was consulted and the index
// — which records openai as the owner — was not.
func TestRouting_OwnershipBeatsDeclarationOrder(t *testing.T) {
	gw := fallbackGateway(t, "groq", "openai")
	groq := claimsEverything("groq")
	openai := ownerOf("openai", "gpt-4o-mini")
	gw.RegisterProvider(groq)
	gw.RegisterProvider(openai)

	resp, err := chat(gw, "gpt-4o-mini")
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if resp.Provider != "openai" {
		t.Errorf("served by %q, want openai", resp.Provider)
	}
	if seen := groq.seen(); len(seen) != 0 {
		t.Errorf("groq received the prompt for %v, want no call — openai owns the model", seen)
	}
}

// TestRouting_DeclaredWildcardTakesOnlyUnownedModels pins the opt-in's
// semantics both ways: a declared wildcard reaches models nobody owns, and is
// skipped for one that has an owner even when it is the first target. That
// ordering — exact match beats wildcard — is what stops the escape hatch from
// reintroducing the defect it exists alongside.
func TestRouting_DeclaredWildcardTakesOnlyUnownedModels(t *testing.T) {
	tests := []struct {
		name      string
		model     string
		wantOwner string
	}{
		{name: "owned model goes to its owner", model: "gpt-4o-mini", wantOwner: "openai"},
		{name: "unowned model goes to the wildcard", model: "my-serving-endpoint", wantOwner: "databricks"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gw := fallbackGateway(t, "databricks", "openai")
			wildcard := &declaredWildcardProvider{claimsEverythingProvider: *claimsEverything("databricks")}
			openai := ownerOf("openai", "gpt-4o-mini")
			gw.RegisterProvider(wildcard)
			gw.RegisterProvider(openai)

			resp, err := chat(gw, tt.model)
			if err != nil {
				t.Fatalf("Route(%q): %v", tt.model, err)
			}
			if resp.Provider != tt.wantOwner {
				t.Errorf("served by %q, want %q", resp.Provider, tt.wantOwner)
			}
		})
	}
}

// TestRouting_UndeclaredWildcardIsNotAWildcard is the property that makes the
// opt-in an opt-in: writing `SupportsModel: return true`, which is what every
// affected provider did, must not be enough. Only the declaration is.
func TestRouting_UndeclaredWildcardIsNotAWildcard(t *testing.T) {
	gw := fallbackGateway(t, "claimer")
	claimer := claimsEverything("claimer")
	gw.RegisterProvider(claimer)

	if _, err := chat(gw, "anything-at-all"); !errors.Is(err, core.ErrNoCapableProvider) {
		t.Fatalf("Route error = %v, want ErrNoCapableProvider", err)
	}
	if seen := claimer.seen(); len(seen) != 0 {
		t.Errorf("provider received the prompt for %v despite declaring no wildcard", seen)
	}
}

// TestFindByModel_MatchesRouting keeps the handlers' pre-flight and the router
// on one answer. FindByModel admitting a model routeTargets then refuses is a
// 404 for a request the gateway advertised, and admitting one nothing owns is
// how the pre-flight used to wave a typo through to three provider calls.
func TestFindByModel_MatchesRouting(t *testing.T) {
	gw := fallbackGateway(t, "claimer", "openai")
	gw.RegisterProvider(claimsEverything("claimer"))
	gw.RegisterProvider(ownerOf("openai", "gpt-4o-mini"))

	if p, ok := gw.FindByModel("gpt-4o-mini"); !ok || p.Name() != "openai" {
		t.Errorf("FindByModel(gpt-4o-mini) = %v, %v; want openai, true", p, ok)
	}
	if p, ok := gw.FindByModel("no-such-model"); ok {
		t.Errorf("FindByModel(no-such-model) resolved to %q, want not found", p.Name())
	}
}

// TestRouting_DiscoveryRestoresAnUncataloguedModel is the inverse gap. A model
// the provider really serves but no catalog carries — qwen3.7-flash — used to
// route only through the catch-all this change removes. Live discovery is the
// named lever that puts it back in the index, so it must be sufficient on its
// own, with no wildcard declared anywhere.
func TestRouting_DiscoveryRestoresAnUncataloguedModel(t *testing.T) {
	const uncatalogued = "qwen3.7-flash"

	gw := fallbackGateway(t, "qwen")
	qwen := ownerOf("qwen", "qwen-plus")
	gw.RegisterProvider(qwen)

	if _, err := chat(gw, uncatalogued); !errors.Is(err, core.ErrNoCapableProvider) {
		t.Fatalf("before discovery: Route error = %v, want ErrNoCapableProvider", err)
	}

	gw.mu.Lock()
	gw.discoveredModels["qwen"] = []providers.ModelInfo{{ID: uncatalogued}}
	gw.rebuildModelIndexesLocked()
	gw.mu.Unlock()

	resp, err := chat(gw, uncatalogued)
	if err != nil {
		t.Fatalf("after discovery: Route: %v", err)
	}
	if resp.Provider != "qwen" {
		t.Errorf("served by %q, want qwen", resp.Provider)
	}
}

// Admission of the requested model happens BEFORE the before_request plugin
// stage, on every routed surface.
//
// The stage is not free. A rate limiter spends a token and a budget spends
// money, so a model no target serves — which can never reach a provider and can
// never cost anything upstream — must not be allowed to spend either on its way
// to a 404. Running the stage first let any caller holding any valid credential
// drain a shared limiter and deny every other caller.

// unroutableModel is a model name no target in these tests serves.
const unroutableModel = "a-model-no-target-serves"

// admissionSurface is one routed surface in the shape these tests drive it:
// something that registers a provider serving a set of models, and something
// that issues one request against that surface.
type admissionSurface struct {
	name     string
	register func(*Gateway, []string)
	call     func(context.Context, *Gateway, string) error
	// routesPluginRequest says the model this surface routes on is the one
	// carried by plugin.Context.Request, so a before_request transform can
	// change it. Every routed surface does now: embeddings and images write the
	// post-stage model back before routing (PRIOR-001), so a transform's
	// rewrite takes effect there too.
	routesPluginRequest bool
}

func admissionSurfaces() []admissionSurface {
	return []admissionSurface{
		{
			name:                "chat",
			routesPluginRequest: true,
			register: func(gw *Gateway, models []string) {
				gw.RegisterProvider(&mockProvider{
					name:   mockProviderName,
					models: models,
					resp:   &providers.Response{ID: "served", Model: models[0], Provider: mockProviderName},
				})
			},
			call: func(ctx context.Context, gw *Gateway, model string) error {
				_, err := gw.Route(ctx, providers.Request{
					Model:    model,
					Messages: []providers.Message{{Role: "user", Content: "hi"}},
				})
				return err
			},
		},
		{
			name:                "streaming",
			routesPluginRequest: true,
			register: func(gw *Gateway, models []string) {
				gw.RegisterProvider(&mockStreamProvider{
					mockProvider: mockProvider{name: mockProviderName, models: models},
				})
			},
			call: func(ctx context.Context, gw *Gateway, model string) error {
				ch, err := gw.RouteStream(ctx, providers.Request{
					Model:    model,
					Messages: []providers.Message{{Role: "user", Content: "hi"}},
					Stream:   true,
				})
				if err != nil {
					return err
				}
				for range ch { //nolint:revive // drain so the meter finishes before the next request
				}
				return nil
			},
		},
		{
			name:                "embeddings",
			routesPluginRequest: true,
			register: func(gw *Gateway, models []string) {
				gw.RegisterProvider(&mockEmbeddingProvider{
					mockProvider: mockProvider{name: mockProviderName, models: models},
				})
			},
			call: func(ctx context.Context, gw *Gateway, model string) error {
				_, err := gw.Embed(ctx, providers.EmbeddingRequest{Model: model, Input: "hi"})
				return err
			},
		},
		{
			name:                "images",
			routesPluginRequest: true,
			register: func(gw *Gateway, models []string) {
				gw.RegisterProvider(&mockImageProvider{
					mockProvider: mockProvider{name: mockProviderName, models: models},
				})
			},
			call: func(ctx context.Context, gw *Gateway, model string) error {
				_, err := gw.GenerateImage(ctx, providers.ImageRequest{Model: model, Prompt: "hi"})
				return err
			},
		},
	}
}

// newAdmissionGateway returns a single-target gateway whose provider serves
// exactly testModel on the given surface.
func newAdmissionGateway(t *testing.T, s admissionSurface) *Gateway {
	t.Helper()
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: mockProviderName}},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	s.register(gw, []string{testModel})
	return gw
}

// A model no target serves must not consume the shared rate limiter, so a valid
// request behind any number of refused ones is still served.
//
// The limiter is the real plugin, configured the way an operator would configure
// it, because the bug is not that some counter moved: it is that a caller could
// deny service to everyone else with requests that cost the gateway nothing.
func TestAdmitModel_UnroutableModelDoesNotSpendTheRateLimitBudget(t *testing.T) {
	const burst = 2
	const refusedRequests = 4

	for _, surface := range admissionSurfaces() {
		t.Run(surface.name, func(t *testing.T) {
			gw := newAdmissionGateway(t, surface)

			limiter := &ratelimit.Plugin{}
			if err := limiter.Init(map[string]any{
				"requests_per_second": burst,
				"burst":               burst,
			}); err != nil {
				t.Fatalf("init rate-limit: %v", err)
			}
			if err := gw.RegisterPlugin(plugin.StageBeforeRequest, limiter); err != nil {
				t.Fatalf("register rate-limit: %v", err)
			}

			ctx := context.Background()
			for i := 1; i <= refusedRequests; i++ {
				err := surface.call(ctx, gw, unroutableModel)
				if !errors.Is(err, core.ErrNoCapableProvider) {
					t.Fatalf("request %d for an unroutable model = %v, want core.ErrNoCapableProvider", i, err)
				}
			}

			// The whole point: the limiter never saw any of the above, so a caller
			// asking for a model the gateway does serve is still served.
			if err := surface.call(ctx, gw, testModel); err != nil {
				t.Fatalf("valid request after %d refused ones: %v", refusedRequests, err)
			}
		})
	}
}

// modelRewritePlugin is a plugin that renames the model at before_request, the
// documented use of a transform (plugin.Plugin.Execute "may mutate them"). The
// type it reports is a parameter because the type is what admission reads.
func modelRewritePlugin(typ plugin.PluginType, from, to string) *testPlugin {
	return &testPlugin{
		name: "model-rewrite",
		typ:  typ,
		execFn: func(_ context.Context, pctx *plugin.Context) error {
			if pctx.Stage == plugin.StageBeforeRequest && pctx.Request != nil && pctx.Request.Model == from {
				pctx.Request.Model = to
			}
			return nil
		},
	}
}

// A transform plugin is exactly the thing that can turn an unroutable model into
// a routable one, so admission ahead of the plugin stage must stand down when
// one is configured: the model the caller sent is not yet the model that will be
// routed. Admitting on the pre-plugin name refused requests that had been served
// for as long as transforms have existed — a team-wide alias rewritten to a real
// model id answered 404 without the provider ever being asked.
func TestAdmitModel_TransformPluginCanStillMakeTheModelRoutable(t *testing.T) {
	for _, surface := range admissionSurfaces() {
		if !surface.routesPluginRequest {
			continue
		}
		t.Run(surface.name, func(t *testing.T) {
			gw := newAdmissionGateway(t, surface)
			if err := gw.RegisterPlugin(plugin.StageBeforeRequest,
				modelRewritePlugin(plugin.TypeTransform, unroutableModel, testModel)); err != nil {
				t.Fatalf("register transform: %v", err)
			}

			if err := surface.call(context.Background(), gw, unroutableModel); err != nil {
				t.Fatalf("request for a model the transform rewrites to %q = %v, want it served", testModel, err)
			}
		})
	}
}

// Which plugins disable admission is decided by the type a plugin REPORTS and by
// the stage it runs at, and by nothing else.
//
// The type is the same authority the fail-open policy reads rather than
// plugins[].type in config, because only one of the two is the code that runs.
// The stage matters just as much: a transform that runs after the request has
// been routed cannot change where it went, so it must not cost the deployment
// the drain protection.
func TestAdmitModel_StandsDownOnlyForABeforeRequestTransform(t *testing.T) {
	tests := []struct {
		name string
		// stage is empty when the case registers no plugin at all.
		stage plugin.Stage
		typ   plugin.PluginType
		// standsDown is whether an unroutable model is admitted, i.e. left for
		// the routing walk to refuse after the plugins have run.
		standsDown bool
	}{
		{name: "no plugins at all"},
		{name: "before_request transform", stage: plugin.StageBeforeRequest, typ: plugin.TypeTransform, standsDown: true},
		{name: "before_request guardrail", stage: plugin.StageBeforeRequest, typ: plugin.TypeGuardrail},
		{name: "before_request ratelimit", stage: plugin.StageBeforeRequest, typ: plugin.TypeRateLimit},
		{name: "before_request logging", stage: plugin.StageBeforeRequest, typ: plugin.TypeLogging},
		{name: "before_request auth", stage: plugin.StageBeforeRequest, typ: plugin.TypeAuth},
		{name: "after_request transform", stage: plugin.StageAfterRequest, typ: plugin.TypeTransform},
		{name: "on_error transform", stage: plugin.StageOnError, typ: plugin.TypeTransform},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gw := newAdmissionGateway(t, admissionSurfaces()[0])
			if tt.stage != "" {
				if err := gw.RegisterPlugin(tt.stage,
					modelRewritePlugin(tt.typ, unroutableModel, testModel)); err != nil {
					t.Fatalf("register plugin: %v", err)
				}
			}

			err := gw.admitModel(context.Background(), gw.plugins, unroutableModel, nil)
			if tt.standsDown && err != nil {
				t.Fatalf("admitModel = %v, want nil — the stage can still rewrite the model", err)
			}
			if !tt.standsDown && !errors.Is(err, core.ErrNoCapableProvider) {
				t.Fatalf("admitModel = %v, want core.ErrNoCapableProvider — nothing here can rewrite the model", err)
			}
		})
	}
}

// recordingWriter is a requestlog.Writer that keeps what the request logger
// persisted, so a test can assert the row exists rather than that some function
// was called.
type recordingWriter struct {
	mu      sync.Mutex
	entries []requestlog.Entry
}

func (w *recordingWriter) Write(_ context.Context, entry requestlog.Entry) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.entries = append(w.entries, entry)
	return nil
}

func (w *recordingWriter) snapshot() []requestlog.Entry {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]requestlog.Entry(nil), w.entries...)
}

// Refusing before the before_request stage must not cost the request its record.
//
// This is the combination the architecture already uses for a policy denial: the
// stage that would have run is skipped, and on_error runs anyway. Without it a
// refused request would leave no request-log row at all, and the surfaces that
// answer 404 most often would be the ones with no trail.
func TestAdmitModel_RefusalSkipsBeforeRequestAndStillRecordsTheRequest(t *testing.T) {
	for _, surface := range admissionSurfaces() {
		t.Run(surface.name, func(t *testing.T) {
			gw := newAdmissionGateway(t, surface)

			beforeRuns := 0
			if err := gw.RegisterPlugin(plugin.StageBeforeRequest, &testPlugin{
				name: "before-spy",
				typ:  plugin.TypeGuardrail,
				execFn: func(context.Context, *plugin.Context) error {
					beforeRuns++
					return nil
				},
			}); err != nil {
				t.Fatalf("register before-spy: %v", err)
			}

			writer := &recordingWriter{}
			reqLogger := &pluginlogger.RequestLogger{}
			reqLogger.SetRequestLogWriter(writer)
			if err := reqLogger.Init(map[string]any{"persist": true}); err != nil {
				t.Fatalf("init request-logger: %v", err)
			}
			if err := gw.RegisterPlugin(plugin.StageOnError, reqLogger); err != nil {
				t.Fatalf("register request-logger: %v", err)
			}

			err := surface.call(context.Background(), gw, unroutableModel)
			if !errors.Is(err, core.ErrNoCapableProvider) {
				t.Fatalf("call = %v, want core.ErrNoCapableProvider", err)
			}

			if beforeRuns != 0 {
				t.Errorf("before_request stage ran %d times for a model no target serves; it must not run at all", beforeRuns)
			}

			entries := writer.snapshot()
			if len(entries) != 1 {
				t.Fatalf("persisted %d request-log rows, want exactly 1 on_error row: %+v", len(entries), entries)
			}
			row := entries[0]
			if row.Stage != string(plugin.StageOnError) {
				t.Errorf("row stage = %q, want %q", row.Stage, plugin.StageOnError)
			}
			if row.Model != unroutableModel {
				t.Errorf("row model = %q, want %q — a row that cannot name what was asked for answers nothing", row.Model, unroutableModel)
			}
			if row.ErrorMessage == "" {
				t.Error("row carries no error message")
			}
		})
	}
}

// declaredWildcardTarget is a provider that serves no enumerable model and
// declares core.AnyModelProvider, the way ollama and databricks do.
type admissionWildcardProvider struct {
	mockProvider
}

func (p *admissionWildcardProvider) SupportsModel(string) bool { return true }
func (p *admissionWildcardProvider) ServesAnyModel()           {}

// Admission may refuse only when it is certain no target serves the model.
//
// A false admit costs nothing — the router still answers the same 404 — while a
// false refusal breaks a working request, so every source that can make a model
// routable is checked here: the provider's own configured models, live
// discovery, targets[].models, and a provider that declares it accepts ids
// nothing can enumerate.
func TestAdmitModel_AdmitsEveryModelSomeTargetCanServe(t *testing.T) {
	tests := []struct {
		name   string
		build  func(*testing.T) *Gateway
		model  string
		admits bool
	}{
		{
			name: "model the provider's own config names",
			build: func(t *testing.T) *Gateway {
				return newAdmissionGateway(t, admissionSurfaces()[0])
			},
			model:  testModel,
			admits: true,
		},
		{
			name: "model no source names",
			build: func(t *testing.T) *Gateway {
				return newAdmissionGateway(t, admissionSurfaces()[0])
			},
			model:  unroutableModel,
			admits: false,
		},
		{
			name: "model declared by targets[].models",
			build: func(t *testing.T) *Gateway {
				gw, err := newTestGateway(t, config.Config{
					Strategy: config.StrategyConfig{Mode: config.ModeSingle},
					Targets: []config.Target{{
						VirtualKey: mockProviderName,
						Models:     []string{"operator-declared-model"},
					}},
				})
				if err != nil {
					t.Fatalf("new gateway: %v", err)
				}
				gw.RegisterProvider(&mockProvider{name: mockProviderName, models: []string{testModel}})
				return gw
			},
			model:  "operator-declared-model",
			admits: true,
		},
		{
			name: "model only live discovery found",
			build: func(t *testing.T) *Gateway {
				gw := newAdmissionGateway(t, admissionSurfaces()[0])
				gw.mu.Lock()
				gw.discoveredModels[mockProviderName] = []providers.ModelInfo{{ID: "discovered-model"}}
				gw.rebuildModelIndexesLocked()
				gw.mu.Unlock()
				return gw
			},
			model:  "discovered-model",
			admits: true,
		},
		{
			name: "any model at all, for a target that declares core.AnyModelProvider",
			build: func(t *testing.T) *Gateway {
				gw, err := newTestGateway(t, config.Config{
					Strategy: config.StrategyConfig{Mode: config.ModeSingle},
					Targets:  []config.Target{{VirtualKey: mockProviderName}},
				})
				if err != nil {
					t.Fatalf("new gateway: %v", err)
				}
				gw.RegisterProvider(&admissionWildcardProvider{
					mockProvider: mockProvider{name: mockProviderName},
				})
				return gw
			},
			model:  "a-name-no-index-could-enumerate",
			admits: true,
		},
		{
			// ValidateConfig rejects a target-less config, so this state is only
			// reachable programmatically — which is exactly when the guard has to
			// hold, since refusing every model is the failure it prevents.
			name: "a config with no targets is a different diagnosis and is not refused here",
			build: func(t *testing.T) *Gateway {
				gw := newAdmissionGateway(t, admissionSurfaces()[0])
				gw.mu.Lock()
				gw.config.Targets = nil
				gw.mu.Unlock()
				return gw
			},
			model:  unroutableModel,
			admits: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gw := tt.build(t)
			err := gw.admitModel(context.Background(), gw.plugins, tt.model, nil)
			if tt.admits && err != nil {
				t.Fatalf("admitModel(%q) = %v, want nil — a false refusal breaks a working request", tt.model, err)
			}
			if !tt.admits && !errors.Is(err, core.ErrNoCapableProvider) {
				t.Fatalf("admitModel(%q) = %v, want core.ErrNoCapableProvider", tt.model, err)
			}
		})
	}
}

// An alias resolves before admission, so a request naming one is never refused
// for a model the caller never sent.
func TestAdmitModel_AliasIsResolvedBeforeAdmission(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: mockProviderName}},
		Aliases:  map[string]string{"fast": testModel},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	gw.RegisterProvider(&mockProvider{
		name:   mockProviderName,
		models: []string{testModel},
		resp:   &providers.Response{ID: "served", Model: testModel, Provider: mockProviderName},
	})

	if _, err := gw.Route(context.Background(), providers.Request{
		Model:    "fast",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	}); err != nil {
		t.Fatalf("Route via alias: %v", err)
	}
}

// The refusal keeps naming the target no provider answered to. That name is the
// whole diagnosis of the usual cause — a credential that has not rolled out —
// and it is deliberately kept out of the response, so it has to reach the log.
func TestAdmitModel_RefusalNamesTheUnregisteredTarget(t *testing.T) {
	logs := captureLogs(t)

	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeFallback},
		Targets:  []config.Target{{VirtualKey: mockProviderName}, {VirtualKey: "ghost"}},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	gw.RegisterProvider(&mockProvider{name: mockProviderName, models: []string{testModel}})

	if err := gw.admitModel(context.Background(), gw.plugins, unroutableModel, nil); !errors.Is(err, core.ErrNoCapableProvider) {
		t.Fatalf("admitModel = %v, want core.ErrNoCapableProvider", err)
	}
	if !strings.Contains(logs.String(), "ghost") {
		t.Errorf("the unresolvable target is missing from the log, leaving nothing to diagnose: %s", logs.String())
	}
}
