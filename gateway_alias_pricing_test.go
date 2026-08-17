package aigateway

import (
	"context"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/pkg/circuitbreaker"
	"github.com/ferro-labs/ai-gateway/providers"
)

// aliasPricingWantCostUSD is 100 prompt tokens at $5/1M plus 50 completion
// tokens at $15/1M, computed against aliasPricingCatalog's one priced entry.
const aliasPricingWantCostUSD = 0.0005 + 0.00075

// aliasPricingCatalog prices only the canonical identity "mock", never an
// alias, so a non-zero cost can only come from a correctly resolved key.
func aliasPricingCatalog() models.Catalog {
	return models.Catalog{
		"mock/" + testModel: {
			Provider: "mock",
			ModelID:  testModel,
			Mode:     models.ModeChat,
			Pricing: models.Pricing{
				InputPerMTokens:  ptrFloat64(5.0),
				OutputPerMTokens: ptrFloat64(15.0),
			},
		},
	}
}

// TestGateway_AliasPricing is issue 396's non-streaming regression suite.
//
// A target registered through RegisterProviderAs under a routing alias
// distinct from its provider's canonical name ("mock") must be priced
// identically to the same provider registered under its canonical name.
//
// gateway_route.go computes cost once (`cost := g.calculateCost(resp)`) and
// feeds the same value to the request logger and the budget plugin, so
// asserting calculateCost's result covers both request-log cost and budget
// spend; asserting the span cost covers the third. resp.Provider must keep
// naming the routing key everywhere attribution reads it.
//
// The mock leaves Response.Provider unset, matching a provider that does not
// stamp its own identity: the shape RegisterProviderAs is documented for,
// and the shape the pipeline's own fallback exists to handle.
func TestGateway_AliasPricing(t *testing.T) {
	tests := []struct {
		name       string
		virtualKey string
	}{
		{name: "canonical name", virtualKey: "mock"},
		{name: "routing alias", virtualKey: "mock--credential-1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gw, err := newTestGateway(t, config.Config{
				Strategy: config.StrategyConfig{Mode: config.ModeSingle},
				Targets:  []config.Target{{VirtualKey: tt.virtualKey}},
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			gw.catalog = aliasPricingCatalog()

			fp := &fakeProvider{}
			gw.SetObservability(fp)

			gw.RegisterProviderAs(tt.virtualKey, &mockProvider{
				name:   "mock",
				models: []string{testModel},
				resp: &providers.Response{
					Model: testModel,
					Usage: providers.Usage{PromptTokens: 100, CompletionTokens: 50},
				},
			})

			resp, err := gw.Route(context.Background(), providers.Request{
				Model:    testModel,
				Messages: []providers.Message{{Role: "user", Content: "hi"}},
			})
			if err != nil {
				t.Fatalf("Route: %v", err)
			}

			if resp.Provider != tt.virtualKey {
				t.Errorf("resp.Provider = %q, want routing key %q", resp.Provider, tt.virtualKey)
			}

			cost := gw.calculateCost(resp)
			if !cost.Priced || cost.TotalUSD != aliasPricingWantCostUSD {
				t.Errorf("calculateCost = %+v, want Priced TotalUSD %.5f", cost, aliasPricingWantCostUSD)
			}

			sp := fp.rootSpan()
			if sp == nil {
				t.Fatal("expected a root span")
			}
			if sp.cost.TotalUSD != aliasPricingWantCostUSD {
				t.Errorf("span cost = %+v, want TotalUSD %.5f", sp.cost, aliasPricingWantCostUSD)
			}
		})
	}
}

// TestGateway_AliasPricing_Streaming is the streaming counterpart of
// TestGateway_AliasPricing.
//
// The cost there is computed inside internal/streamwrap.Meter from
// MeterMeta.PriceProvider rather than from a Response the provider built, so
// this exercises a materially different code path even though the assertion
// -- the span cost matches the canonical price -- is the same shape.
func TestGateway_AliasPricing_Streaming(t *testing.T) {
	tests := []struct {
		name       string
		virtualKey string
	}{
		{name: "canonical name", virtualKey: "mock"},
		{name: "routing alias", virtualKey: "mock--credential-1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gw, err := newTestGateway(t, config.Config{
				Strategy: config.StrategyConfig{Mode: config.ModeSingle},
				Targets:  []config.Target{{VirtualKey: tt.virtualKey}},
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			gw.catalog = aliasPricingCatalog()

			fp := &fakeProvider{}
			gw.SetObservability(fp)

			streamCh := make(chan providers.StreamChunk, 1)
			streamCh <- providers.StreamChunk{
				Usage: &providers.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150},
			}
			close(streamCh)

			gw.RegisterProviderAs(tt.virtualKey, &mockStreamProvider{
				mockProvider: mockProvider{name: "mock", models: []string{testModel}},
				streamCh:     streamCh,
			})

			ch, err := gw.RouteStream(context.Background(), providers.Request{
				Model:    testModel,
				Stream:   true,
				Messages: []providers.Message{{Role: "user", Content: "hi"}},
			})
			if err != nil {
				t.Fatalf("RouteStream: %v", err)
			}
			drainStream(t, ch)

			sp := fp.rootSpan()
			if sp == nil {
				t.Fatal("expected a root span")
			}
			if sp.cost.TotalUSD != aliasPricingWantCostUSD {
				t.Errorf("stream span cost = %+v, want TotalUSD %.5f", sp.cost, aliasPricingWantCostUSD)
			}
		})
	}
}

// TestGateway_AliasPricing_CostOptimizedStrategy is issue 396's third path, and
// the one the strategy-level unit tests could not reach.
//
// internal/strategies exercises SelectTargets against a lookup that hands back
// the raw providers.WithName wrapper. The gateway does not: getStrategy wraps
// every provider in the model-index view and then in the concurrency limiter
// and circuit breaker, and CanonicalName has to survive all three to reach the
// vendor the catalog is keyed on. It did not, so an aliased target priced as
// unpriced and `unpriced_strategy: skip` dropped it from routing entirely --
// exactly the defect 396 reports, passing its own unit test the whole time.
//
// This asserts through gateway wiring rather than a hand-built lookup, so a
// future decorator that forgets core.IdentityUnwrapper fails here.
func TestGateway_AliasPricing_CostOptimizedStrategy(t *testing.T) {
	tests := []struct {
		name       string
		virtualKey string
	}{
		{name: "canonical name", virtualKey: "mock"},
		{name: "routing alias", virtualKey: "mock--credential-1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gw, err := newTestGateway(t, config.Config{
				Strategy: config.StrategyConfig{
					Mode: config.ModeCostOptimized,
					// skip is the setting that turns mis-pricing from a
					// wrong ORDER into a dropped target, so it is the one
					// that fails loudly when the identity is lost.
					UnpricedStrategy: config.UnpricedStrategySkip,
				},
				Targets: []config.Target{{VirtualKey: tt.virtualKey}},
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			gw.catalog = aliasPricingCatalog()

			gw.RegisterProviderAs(tt.virtualKey, &mockProvider{
				name:   "mock",
				models: []string{testModel},
			})

			strategy, err := gw.getStrategy()
			if err != nil {
				t.Fatalf("getStrategy: %v", err)
			}

			keys, err := strategy.SelectTargets(providers.Request{
				Model:    testModel,
				Messages: []providers.Message{{Role: "user", Content: "hi"}},
			})
			if err != nil {
				t.Fatalf("SelectTargets: the target is priced through its canonical identity, so skip mode must rank it: %v", err)
			}
			if len(keys) == 0 || keys[0] != tt.virtualKey {
				t.Fatalf("SelectTargets = %v, want %q to lead", keys, tt.virtualKey)
			}
		})
	}
}

// TestCanonicalNameSurvivesRoutingDecorators pins the seam itself, one layer at
// a time, so a regression names the decorator that lost the identity rather
// than surfacing as a mis-priced route several packages away.
// The limiter and breaker layers are composed with decorateProvider rather than
// built by hand, so the test covers the ORDER production applies as well as the
// seam itself -- breaker outermost, limiter innermost.
func TestCanonicalNameSurvivesRoutingDecorators(t *testing.T) {
	const (
		canonical = "mock"
		alias     = "mock--credential-1"
	)
	raw := &mockProvider{name: canonical, models: []string{testModel}}
	named := providers.WithName(raw, alias)

	indexed := withIndexedModels(alias, named, map[string][]string{})
	lim := newProviderLimiter(1, 1)
	cb := circuitbreaker.New(1, 1, 1, time.Minute)

	for _, tc := range []struct {
		layer string
		p     providers.Provider
	}{
		{"WithName", named},
		{"+ indexed view", indexed},
		{"+ concurrency limiter", decorateProvider(alias, indexed, nil, lim)},
		{"+ circuit breaker", decorateProvider(alias, indexed, cb, nil)},
		{"+ both, as getStrategy composes them", decorateProvider(alias, indexed, cb, lim)},
	} {
		if got := providers.CanonicalName(tc.p); got != canonical {
			t.Errorf("%s: CanonicalName = %q, want %q", tc.layer, got, canonical)
		}
	}
}
