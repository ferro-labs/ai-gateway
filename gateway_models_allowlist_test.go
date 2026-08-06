package aigateway

import (
	"errors"
	"testing"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/providers"
)

// TestAllModels_ListsOnlyTargetedProviders is the finding: a gateway whose only
// target was groq listed 193 models, 179 of them owned by openai and cohere —
// registered because their credentials happened to be in the environment, named
// by no target, and answered 404 by every routed surface the moment a client
// asked for one.
//
// The listing is what a client reads to decide what to send. It has to describe
// the same gateway the router does.
func TestAllModels_ListsOnlyTargetedProviders(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "groq"}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// All three are registered, exactly as they are when three credentials are
	// present. Only one is a target.
	for _, name := range []string{"groq", "openai", "cohere"} {
		gw.RegisterProvider(&mockProvider{
			name:   name,
			models: []string{name + "-local-model"},
			resp:   &providers.Response{ID: name},
		})
	}

	listed := gw.AllModels()
	if len(listed) == 0 {
		t.Fatal("precondition: the listing is empty, so nothing below is being tested")
	}

	for _, m := range listed {
		if m.OwnedBy != "groq" {
			t.Errorf("model %q is listed as owned by %q, which no target names", m.ID, m.OwnedBy)
		}
	}

	// Non-vacuity in the other direction: the target's own inventory must
	// survive intact. It is the catalog set — modelsFor is a precedence, so a
	// catalogued provider's declared list does not add to it — and the filter
	// must not narrow it.
	got := modelsOwnedBy(listed, "groq")
	want := gw.Catalog().ModelsForProvider("groq")
	if len(want) == 0 {
		t.Fatal("precondition: the catalog has no groq models")
	}
	if !equalStrings(got, want) {
		t.Errorf("groq contributes %d models to the listing, want its full catalog set of %d",
			len(got), len(want))
	}
}

// TestAllModels_EveryListedModelRoutes is the property the filter exists to
// restore, asserted end to end rather than by counting: nothing advertised may
// be refused.
func TestAllModels_EveryListedModelRoutes(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeFallback},
		Targets:  []config.Target{{VirtualKey: "groq"}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, name := range []string{"groq", "openai"} {
		gw.RegisterProvider(&mockProvider{
			name:   name,
			models: []string{name + "-local-model"},
			resp:   &providers.Response{ID: name, Provider: name},
		})
	}

	listed := gw.AllModels()
	if len(listed) == 0 {
		t.Fatal("precondition: the listing is empty")
	}
	for _, m := range listed {
		if _, err := gw.Route(t.Context(), providers.Request{
			Model:    m.ID,
			Messages: []providers.Message{{Role: "user", Content: "ping"}},
		}); err != nil {
			t.Fatalf("listed model %q (owned_by %s) is not routable: %v", m.ID, m.OwnedBy, err)
		}
	}
}

// TestAllModels_UnaffectedByAnOpenCircuit draws the line between the two
// questions the word "routable" covers.
//
// Readiness gates on the circuit because it answers "should traffic come here
// now". The catalog answers "what does this gateway serve", and a breaker is a
// transient statement about one target's health. A model that disappeared from
// /v1/models while a breaker was open would read to a client as "this gateway
// does not serve that model" and send it to change its request or its config
// over an outage that clears itself.
func TestAllModels_UnaffectedByAnOpenCircuit(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets: []config.Target{{
			VirtualKey:     "groq",
			CircuitBreaker: &config.CircuitBreakerConfig{FailureThreshold: 1},
		}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.RegisterProvider(&mockProvider{
		name:   "groq",
		models: []string{"groq-local-model"},
		err:    errors.New("upstream down"),
	})

	before := len(gw.AllModels())
	if before == 0 {
		t.Fatal("precondition: the listing is empty")
	}

	// One failure at threshold 1 opens the circuit.
	if _, routeErr := gw.Route(t.Context(), providers.Request{
		Model:    "groq-local-model",
		Messages: []providers.Message{{Role: "user", Content: "ping"}},
	}); routeErr == nil {
		t.Fatal("precondition: the failing provider answered successfully")
	}
	if r := gw.Readiness(); r.Ready {
		t.Fatal("precondition: the circuit did not open, so this asserts nothing")
	}

	if after := len(gw.AllModels()); after != before {
		t.Errorf("listing dropped from %d models to %d because a breaker opened; a catalog is not a health report",
			before, after)
	}
}
