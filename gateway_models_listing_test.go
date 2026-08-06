package aigateway

import (
	"slices"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/internal/strategies"
	"github.com/ferro-labs/ai-gateway/providers"
)

// collidingModelID is a real catalog id for openai and nothing else, so a target
// on any other provider can only come to serve it by declaring it — which is the
// collision these cases are about.
const collidingModelID = "gpt-4o"

// newListingGateway wires the given targets and registers a bare stub for each
// distinct virtual key. The stubs answer SupportsModel false for everything, so
// each model listed below comes from the catalog or from targets[].models and
// never from the provider's own opinion.
func newListingGateway(t *testing.T, targets []config.Target, registered ...string) *Gateway {
	t.Helper()
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeFallback},
		Targets:  targets,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, name := range registered {
		gw.RegisterProvider(&mockProvider{
			name: name,
			resp: &providers.Response{ID: "resp", Model: collidingModelID},
		})
	}
	return gw
}

// TestServedProviders pins the one authority every inventory surface answers
// from: the registered providers a configured target names.
//
// Both halves are separate facts. A provider with a credential and no target
// serves nothing — targets is the allowlist. A target whose provider has no
// credential serves nothing either — it is not registered. Answering from
// either set alone describes providers no request can reach, which is exactly
// what /v1/capabilities did when it read the registered set.
func TestServedProviders(t *testing.T) {
	tests := []struct {
		name       string
		targets    []config.Target
		registered []string
		want       []string
	}{
		{
			name:       "a registered provider no target names is not served",
			targets:    []config.Target{{VirtualKey: "deepseek"}},
			registered: []string{"deepseek", "openai", "groq"},
			want:       []string{"deepseek"},
		},
		{
			name:       "a target whose provider is unregistered is not served",
			targets:    []config.Target{{VirtualKey: "deepseek"}, {VirtualKey: "groq"}},
			registered: []string{"deepseek"},
			want:       []string{"deepseek"},
		},
		{
			name:       "target order is preserved, not registration order",
			targets:    []config.Target{{VirtualKey: "groq"}, {VirtualKey: "deepseek"}},
			registered: []string{"deepseek", "groq"},
			want:       []string{"groq", "deepseek"},
		},
		{
			name:       "two targets on one provider are served once",
			targets:    []config.Target{{VirtualKey: "deepseek"}, {VirtualKey: "deepseek", Models: []string{"x"}}},
			registered: []string{"deepseek"},
			want:       []string{"deepseek"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gw := newListingGateway(t, tt.targets, tt.registered...)
			if got := gw.ServedProviders(); !slices.Equal(got, tt.want) {
				t.Errorf("ServedProviders() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestAllModels_OneEntryPerModelID pins /v1/models to the OpenAI contract it
// serves: the list is keyed by `id`.
//
// Two targets can serve one id — here the catalog knows gpt-4o for openai and
// targets[].models declares it on deepseek. Listing it twice is not more
// information: a client that builds a map by id keeps whichever entry came
// last, so the same request would be described by openai's context window on
// one run and by deepseek's absent metadata on another. One entry is published
// and it names the first configured target, so owned_by describes the target
// the request is offered to first.
func TestAllModels_OneEntryPerModelID(t *testing.T) {
	tests := []struct {
		name    string
		targets []config.Target
		wantOwn string
	}{
		{
			name: "the catalog target listed first owns the id",
			targets: []config.Target{
				{VirtualKey: "openai"},
				{VirtualKey: "deepseek", Models: []string{collidingModelID}},
			},
			wantOwn: "openai",
		},
		{
			name: "the declaring target listed first owns the id",
			targets: []config.Target{
				{VirtualKey: "deepseek", Models: []string{collidingModelID}},
				{VirtualKey: "openai"},
			},
			wantOwn: "deepseek",
		},
		{
			name: "the same id declared on two targets is listed once",
			targets: []config.Target{
				{VirtualKey: "groq", Models: []string{collidingModelID}},
				{VirtualKey: "deepseek", Models: []string{collidingModelID}},
			},
			wantOwn: "groq",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gw := newListingGateway(t, tt.targets, "openai", "deepseek", "groq")

			listed := gw.AllModels()
			seen := map[string]int{}
			for _, m := range listed {
				seen[m.ID]++
			}
			var duplicated []string
			for id, n := range seen {
				if n > 1 {
					duplicated = append(duplicated, id)
				}
			}
			if len(duplicated) > 0 {
				slices.Sort(duplicated)
				t.Errorf("AllModels() lists %d entries for %d ids; duplicated: %v",
					len(listed), len(seen), duplicated)
			}

			idx := slices.IndexFunc(listed, func(m providers.ModelInfo) bool { return m.ID == collidingModelID })
			if idx < 0 {
				t.Fatalf("precondition: %q is not advertised at all", collidingModelID)
			}
			if got := listed[idx].OwnedBy; got != tt.wantOwn {
				t.Errorf("%q owned_by = %q, want %q (the first configured target that serves it)",
					collidingModelID, got, tt.wantOwn)
			}
		})
	}
}

// gemini is used throughout this file because it is the shape the declared list
// exists for: a real catalog-backed provider that implements neither
// DiscoveryProvider nor ConfiguredModelProvider, so before targets[].models the
// catalog was the only thing that could name a model for it and a model newer
// than the catalog was unroutable with no migration available.
const declaredTargetProvider = "gemini"

// declaredModelID is deliberately not a real model name so no catalog revision
// can ever supply it, making every assertion below about the declared list
// rather than about catalog coverage.
const declaredModelID = "gemini-declared-only-test-model"

// newDeclaredModelsGateway wires one target for declaredTargetProvider carrying
// the given declared list, and registers a stub that answers for nothing on its
// own — its SupportsModel is false for every id here, so a route that succeeds
// succeeded through the index.
func newDeclaredModelsGateway(t *testing.T, declared []string) (*Gateway, *mockProvider) {
	t.Helper()
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: declaredTargetProvider, Models: declared}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p := &mockProvider{
		name: declaredTargetProvider,
		resp: &providers.Response{ID: "resp", Model: declaredModelID},
	}
	gw.RegisterProvider(p)
	return gw, p
}

func routes(t *testing.T, gw *Gateway, model string) bool {
	t.Helper()
	_, err := gw.Route(t.Context(), providers.Request{
		Model:    model,
		Messages: []providers.Message{{Role: roleUser, Content: "ping"}},
	})
	return err == nil
}

func advertises(gw *Gateway, model string) bool {
	return slices.ContainsFunc(gw.AllModels(), func(m providers.ModelInfo) bool { return m.ID == model })
}

// TestTargetModelsRoutability is the regression guard in both directions: a
// model an operator declares becomes routable, and a model nobody declared stays
// refused. The second half is the part that must not regress — model ownership
// being authoritative is what stopped an unknown model name from being offered
// to every provider in turn with the prompt body attached.
func TestTargetModelsRoutability(t *testing.T) {
	tests := []struct {
		name       string
		declared   []string
		request    string
		wantRouted bool
	}{
		{
			name:       "declared model routes",
			declared:   []string{declaredModelID},
			request:    declaredModelID,
			wantRouted: true,
		},
		{
			name:       "undeclared model is refused",
			declared:   nil,
			request:    declaredModelID,
			wantRouted: false,
		},
		{
			name:       "declaring one model does not admit its neighbours",
			declared:   []string{declaredModelID},
			request:    declaredModelID + "-sibling",
			wantRouted: false,
		},
		{
			name:       "one of several declared models routes",
			declared:   []string{declaredModelID + "-a", declaredModelID, declaredModelID + "-b"},
			request:    declaredModelID,
			wantRouted: true,
		},
		{
			name:       "a model the catalog already knows still routes",
			declared:   []string{declaredModelID},
			request:    "gemini-2.0-flash",
			wantRouted: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gw, p := newDeclaredModelsGateway(t, tt.declared)
			if p.SupportsModel(tt.request) {
				t.Fatalf("precondition: the stub claims %q itself, so this case would pass without the index", tt.request)
			}
			if got := routes(t, gw, tt.request); got != tt.wantRouted {
				t.Fatalf("routable(%q) = %v, want %v", tt.request, got, tt.wantRouted)
			}
		})
	}
}

// TestTargetModelsAdvertised checks the listing side. A declaration the operator
// can read in their own config file must be visible in /v1/models, or it routes
// while staying invisible to every client that enumerates before it asks.
func TestTargetModelsAdvertised(t *testing.T) {
	gw, _ := newDeclaredModelsGateway(t, []string{declaredModelID})

	if !advertises(gw, declaredModelID) {
		t.Errorf("/v1/models omits declared model %q", declaredModelID)
	}

	// The catalog's own coverage survives: the declared list adds, it does not
	// replace. Without this a one-line declaration would unadvertise the other
	// forty-odd models the target serves.
	catalogModels := 0
	for _, m := range gw.AllModels() {
		if m.ID != declaredModelID {
			catalogModels++
		}
	}
	if catalogModels == 0 {
		t.Errorf("declaring one model left the target advertising only that model")
	}

	// Every advertised model is routable — the invariant modelsFor has always
	// kept, restated because the declared list is now part of both sets.
	for _, m := range gw.AllModels() {
		if !routes(t, gw, m.ID) {
			t.Errorf("advertised model %q is not routable", m.ID)
		}
	}
}

// TestTargetModelsAdvertisedOnce guards the merge: declaring an id the catalog
// already reports is meant to be a harmless no-op, not a doubled listing.
func TestTargetModelsAdvertisedOnce(t *testing.T) {
	gw, _ := newDeclaredModelsGateway(t, nil)
	fromCatalog := gw.AllModels()
	if len(fromCatalog) == 0 {
		t.Skip("catalog has no models for " + declaredTargetProvider)
	}
	existing := fromCatalog[0].ID

	gw2, _ := newDeclaredModelsGateway(t, []string{existing})
	seen := 0
	for _, m := range gw2.AllModels() {
		if m.ID == existing {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("model %q declared and also in the catalog is advertised %d times, want 1", existing, seen)
	}
	if got, want := len(gw2.AllModels()), len(fromCatalog); got != want {
		t.Errorf("declaring an already-known model changed the advertised count: got %d, want %d", got, want)
	}
}

// TestTargetModelsIsolatedPerTarget keeps a declaration on the target that made
// it. A list that leaked across targets would hand one provider's prompt bodies
// to another, which is the failure ownership was made authoritative to stop.
func TestTargetModelsIsolatedPerTarget(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeFallback},
		Targets: []config.Target{
			{VirtualKey: "gemini", Models: []string{declaredModelID}},
			{VirtualKey: "cohere"},
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, name := range []string{"gemini", "cohere"} {
		gw.RegisterProvider(&mockProvider{name: name, resp: &providers.Response{ID: name}})
	}

	owners := gw.routing().index.exactProviders[declaredModelID]
	if !slices.Equal(owners, []string{"gemini"}) {
		t.Fatalf("declared model %q is owned by %v, want only [gemini]", declaredModelID, owners)
	}
	for _, m := range gw.AllModels() {
		if m.ID == declaredModelID && m.OwnedBy != "gemini" {
			t.Errorf("declared model advertised as owned by %q", m.OwnedBy)
		}
	}
}

// TestTargetModelsSurviveReload covers the reload seam. Targets change on
// reload and the routing index is built from them, so a reload that does not
// republish leaves routing answering from the config the operator just replaced.
func TestTargetModelsSurviveReload(t *testing.T) {
	added := declaredModelID + "-added"

	gw, _ := newDeclaredModelsGateway(t, []string{declaredModelID})
	if routes(t, gw, added) {
		t.Fatalf("precondition: %q routes before it is declared", added)
	}

	reload := func(models []string) {
		t.Helper()
		if err := gw.ReloadConfig(t.Context(), config.Config{
			Strategy: config.StrategyConfig{Mode: config.ModeSingle},
			Targets:  []config.Target{{VirtualKey: declaredTargetProvider, Models: models}},
		}); err != nil {
			t.Fatalf("ReloadConfig: %v", err)
		}
	}

	reload([]string{declaredModelID, added})
	if !routes(t, gw, added) {
		t.Errorf("%q does not route after a reload declared it", added)
	}
	if !advertises(gw, added) {
		t.Errorf("%q is not advertised after a reload declared it", added)
	}

	reload(nil)
	if routes(t, gw, added) {
		t.Errorf("%q still routes after a reload withdrew every declaration", added)
	}
	if advertises(gw, added) {
		t.Errorf("%q is still advertised after a reload withdrew every declaration", added)
	}
}

// TestTargetModelsForUnregisteredProvider covers a target whose provider this
// process never registered — the config that names a provider whose credential
// has not rolled out yet. It must not synthesise routing for a provider that
// does not exist, and it must not panic.
func TestTargetModelsForUnregisteredProvider(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeFallback},
		Targets: []config.Target{
			{VirtualKey: declaredTargetProvider, Models: []string{declaredModelID}},
			{VirtualKey: "cohere", Models: []string{"cohere-declared-only-test-model"}},
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Only gemini registers; cohere's credential is absent.
	gw.RegisterProvider(&mockProvider{name: declaredTargetProvider, resp: &providers.Response{ID: "resp"}})

	if owners := gw.routing().index.exactProviders["cohere-declared-only-test-model"]; len(owners) != 0 {
		t.Errorf("model declared for an unregistered provider has owners %v, want none", owners)
	}
	if advertises(gw, "cohere-declared-only-test-model") {
		t.Errorf("model declared for an unregistered provider is advertised")
	}
	if !routes(t, gw, declaredModelID) {
		t.Errorf("the registered target's declared model stopped routing")
	}
}

// advertisedRoutableProviders are catalog-backed providers wired as a fallback
// chain the way a default config wires them. Several are used rather than one so
// a divergence confined to a single provider's model set still surfaces.
var advertisedRoutableProviders = []string{"openai", "anthropic", "gemini", "groq", "mistral", "deepseek"}

// declaredOnlySuffix builds the single model ID a stub provider declares. It is
// deliberately absent from the catalog, so every model the gateway advertises
// for that provider comes from the index and none from the provider's own
// ConfiguredModels — the narrow-provider shape that makes this test non-vacuous.
const declaredOnlySuffix = "-declared-only"

// TestAdvertisedModelsAreRoutable is the drift guard between what the gateway
// tells clients it serves and what it will actually route. /v1/models and the
// admission check answer from the model index — declared models plus catalog
// plus live discovery — while the strategies select a target on the provider's
// own SupportsModel, which is usually a much narrower hardcoded list. When the
// two diverge the gateway advertises a model and then refuses it with a 500,
// which is a defect no per-provider test catches because each provider is
// individually correct.
func TestAdvertisedModelsAreRoutable(t *testing.T) {
	targets := make([]config.Target, len(advertisedRoutableProviders))
	for i, name := range advertisedRoutableProviders {
		targets[i] = config.Target{VirtualKey: name}
	}
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeFallback},
		Targets:  targets,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, name := range advertisedRoutableProviders {
		gw.RegisterProvider(&mockProvider{
			name:   name,
			models: []string{name + declaredOnlySuffix},
			resp:   &providers.Response{ID: name, Model: name + declaredOnlySuffix},
		})
	}

	advertised := gw.AllModels()
	if len(advertised) < len(advertisedRoutableProviders) {
		t.Fatalf("precondition: gateway advertises only %d models across %d providers",
			len(advertised), len(advertisedRoutableProviders))
	}

	// Non-vacuity: no stub declares any advertised model, so every route below
	// succeeds only because routing consults the same index the listing does.
	for _, m := range advertised {
		p, ok := gw.GetProvider(m.OwnedBy)
		if !ok {
			t.Fatalf("advertised model %q is owned by unregistered provider %q", m.ID, m.OwnedBy)
		}
		if p.SupportsModel(m.ID) {
			t.Fatalf("precondition: provider %q declares advertised model %q, so this test would pass "+
				"even with routing reading only ConfiguredModels", m.OwnedBy, m.ID)
		}
	}

	configured := make(map[string]bool, len(advertisedRoutableProviders))
	for _, name := range advertisedRoutableProviders {
		configured[name] = true
	}

	var refused []string
	for _, m := range advertised {
		resp, err := gw.Route(t.Context(), providers.Request{
			Model:    m.ID,
			Messages: []providers.Message{{Role: "user", Content: "ping"}},
		})
		switch {
		case err != nil:
			refused = append(refused, m.ID+" (owned_by "+m.OwnedBy+"): "+err.Error())
		case !configured[resp.Provider]:
			refused = append(refused, m.ID+" (owned_by "+m.OwnedBy+"): routed to unconfigured target "+resp.Provider)
		}
	}
	if len(refused) > 0 {
		shown := refused
		if len(shown) > 5 {
			shown = shown[:5]
		}
		t.Fatalf("%d of %d advertised models are not routable — the gateway lists them on /v1/models "+
			"and then refuses them. The routing view of a provider's model set must stay the union the "+
			"listing answers from (declared + catalog + discovery), not its declared slice alone.\nFirst failures:\n  %s",
			len(refused), len(advertised), strings.Join(shown, "\n  "))
	}
}

// TestAdvertisedModelsAreRoutable_EveryStrategyMode holds the same invariant
// under every routing mode, which is where it used to break.
//
// A strategy refusing a model outright is only one of the two ways the listing
// can overstate. The other is a mode that COMMITS to one target: single,
// conditional and content-based attempt the target they name and no other, so a
// model owned only by a different configured target is a 404 — and every one of
// them was advertised, because the listing asked whether the strategy offered
// any target rather than whether it offered one that serves the model.
//
// wantOwners pins the narrowing from the other side. Answering "nothing is
// routable" would satisfy the invariant and serve nobody, so each mode also
// states which targets may own an advertised model.
func TestAdvertisedModelsAreRoutable_EveryStrategyMode(t *testing.T) {
	const first, second = "openai", "groq"

	tests := []struct {
		name     string
		strategy config.StrategyConfig
		// wantOwners is the set of target keys that may own an advertised model.
		wantOwners []string
	}{
		{
			name:       "single attempts the first target and nothing else",
			strategy:   config.StrategyConfig{Mode: config.ModeSingle},
			wantOwners: []string{first},
		},
		{
			name:       "fallback walks every target",
			strategy:   config.StrategyConfig{Mode: config.ModeFallback},
			wantOwners: []string{first, second},
		},
		{
			name:       "loadbalance chooses from the targets that serve the model",
			strategy:   config.StrategyConfig{Mode: config.ModeLoadBalance},
			wantOwners: []string{first, second},
		},
		{
			name:       "least-latency chooses from the targets that serve the model",
			strategy:   config.StrategyConfig{Mode: config.ModeLatency},
			wantOwners: []string{first, second},
		},
		{
			name: "cost-optimized allowing unpriced models",
			strategy: config.StrategyConfig{
				Mode:             config.ModeCostOptimized,
				UnpricedStrategy: config.UnpricedStrategyAllow,
			},
			wantOwners: []string{first, second},
		},
		{
			// Conditional rules match on the model, which is exactly what the
			// listing asks about, so its answer is exact: the rule's target keeps
			// its models advertised and the fallback target keeps its own.
			name: "conditional keeps the models its rule routes",
			strategy: config.StrategyConfig{
				Mode: config.ModeConditional,
				Conditions: []config.Condition{{
					Key:       config.ConditionKeyModelPrefix,
					Value:     "llama",
					TargetKey: second,
				}},
			},
			wantOwners: []string{first, second},
		},
		{
			// Content-based rules match on the prompt, which a model listing does
			// not have, so the answer is what a request carrying no content gets:
			// the no-match fallback target. A model only a matched rule would
			// reach is not advertised — the safe direction, and the only one
			// available to an endpoint that answers before any prompt exists.
			name: "content-based reports the no-match target",
			strategy: config.StrategyConfig{
				Mode: config.ModeContentBased,
				ContentConditions: []config.ContentCondition{{
					Type:      string(strategies.PromptContains),
					Value:     "code",
					TargetKey: second,
				}},
			},
			wantOwners: []string{first},
		},
		{
			name: "ab-test draws from the variants that serve the model",
			strategy: config.StrategyConfig{
				Mode: config.ModeABTest,
				ABVariants: []config.ABVariantConfig{
					{TargetKey: first, Weight: 50, Label: "control"},
					{TargetKey: second, Weight: 50, Label: "variant"},
				},
			},
			wantOwners: []string{first, second},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gw, err := newTestGateway(t, config.Config{
				Strategy: tt.strategy,
				Targets: []config.Target{
					{VirtualKey: first, Weight: 1},
					{VirtualKey: second, Weight: 1},
				},
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			for _, name := range []string{first, second} {
				gw.RegisterProvider(&mockProvider{
					name:   name,
					models: []string{name + declaredOnlySuffix},
					resp:   &providers.Response{ID: name},
				})
			}

			advertised := gw.AllModels()
			if len(advertised) == 0 {
				t.Fatal("the gateway advertises nothing at all")
			}

			owners := map[string]bool{}
			var refused []string
			for _, m := range advertised {
				owners[m.OwnedBy] = true
				if _, routeErr := gw.Route(t.Context(), providers.Request{
					Model:    m.ID,
					Messages: []providers.Message{{Role: roleUser, Content: "ping"}},
				}); routeErr != nil {
					refused = append(refused, m.ID+" (owned_by "+m.OwnedBy+"): "+routeErr.Error())
				}
			}

			if len(refused) > 0 {
				shown := refused
				if len(shown) > 5 {
					shown = shown[:5]
				}
				t.Errorf("%d of %d advertised models are refused under mode %s — the listing must not "+
					"advertise a model the walk will not reach.\n  %s",
					len(refused), len(advertised), tt.strategy.Mode, strings.Join(shown, "\n  "))
			}

			for _, want := range tt.wantOwners {
				if !owners[want] {
					t.Errorf("no advertised model is owned by %q under mode %s; the listing narrowed past "+
						"what this mode routes", want, tt.strategy.Mode)
				}
			}
			for owner := range owners {
				if !slices.Contains(tt.wantOwners, owner) {
					t.Errorf("models owned by %q are advertised under mode %s, which never attempts that target",
						owner, tt.strategy.Mode)
				}
			}
		})
	}
}

// skipDeclaredModelID is absent from every catalog revision, which is the whole
// point: a declared model exists precisely because no automatic source names it,
// so it can never carry a catalog price.
const skipDeclaredModelID = "deepseek-declared-2027-test-model"

// TestAdvertisedModelsAreRoutable_UnpricedStrategy holds the same invariant
// under the one strategy that refuses a model outright.
//
// cost-optimized with unpriced_strategy: skip serves only what the catalog
// prices. Every declared model is unpriced by construction, and a catalog model
// its provider has no price for is unpriced too — so both were advertised and
// both 404'd, which is the shape a client cannot recover from: it read the
// gateway's own inventory and was refused by it.
//
// The allow case is not decoration. It is what makes the skip case mean
// something: the same declared model, the same stub, advertised and routable,
// so a skip run that lists nothing extra is the strategy talking and not an
// unrelated gap in the fixture.
func TestAdvertisedModelsAreRoutable_UnpricedStrategy(t *testing.T) {
	tests := []struct {
		name             string
		unpricedStrategy string
		wantDeclared     bool
	}{
		{
			name:             "skip does not advertise what it will not price",
			unpricedStrategy: config.UnpricedStrategySkip,
			wantDeclared:     false,
		},
		{
			name:             "allow advertises and routes the declared model",
			unpricedStrategy: config.UnpricedStrategyAllow,
			wantDeclared:     true,
		},
		{
			name:             "fallback advertises and routes the declared model",
			unpricedStrategy: config.UnpricedStrategyFallback,
			wantDeclared:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			targets := []config.Target{
				{VirtualKey: "deepseek", Models: []string{skipDeclaredModelID}},
				{VirtualKey: "groq"},
			}
			gw, err := newTestGateway(t, config.Config{
				Strategy: config.StrategyConfig{
					Mode:             config.ModeCostOptimized,
					UnpricedStrategy: tt.unpricedStrategy,
				},
				Targets: targets,
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			for _, name := range []string{"deepseek", "groq"} {
				gw.RegisterProvider(&mockProvider{
					name: name,
					resp: &providers.Response{ID: name, Model: skipDeclaredModelID},
				})
			}

			advertised := gw.AllModels()
			if len(advertised) == 0 {
				t.Fatal("precondition: the gateway advertises nothing at all")
			}

			declared := false
			var refused []string
			for _, m := range advertised {
				if m.ID == skipDeclaredModelID {
					declared = true
				}
				if _, routeErr := gw.Route(t.Context(), providers.Request{
					Model:    m.ID,
					Messages: []providers.Message{{Role: roleUser, Content: "ping"}},
				}); routeErr != nil {
					refused = append(refused, m.ID+" (owned_by "+m.OwnedBy+"): "+routeErr.Error())
				}
			}

			if declared != tt.wantDeclared {
				t.Errorf("declared model %q advertised = %v, want %v", skipDeclaredModelID, declared, tt.wantDeclared)
			}
			if len(refused) > 0 {
				shown := refused
				if len(shown) > 5 {
					shown = shown[:5]
				}
				t.Fatalf("%d of %d advertised models are refused under unpriced_strategy: %s.\n  %s",
					len(refused), len(advertised), tt.unpricedStrategy, strings.Join(shown, "\n  "))
			}
		})
	}
}
