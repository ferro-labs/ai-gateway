//go:build integration
// +build integration

package strategies_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/providers/core"
	"github.com/ferro-labs/ai-gateway/test/testutil"
)

// routeOnce builds a gateway from cfg, registers the given providers, and
// routes one request for stratModel.
func routeOnce(t *testing.T, cfg config.Config, prompt string, provs ...*miniStub) (*core.Response, error) {
	t.Helper()

	gw, err := testutil.NewTestGateway(t, cfg)
	if err != nil {
		t.Fatalf("aigateway.New: %v", err)
	}
	for _, p := range provs {
		gw.RegisterProvider(p)
	}
	return gw.Route(t.Context(), core.Request{
		Model:    stratModel,
		Messages: []core.Message{{Role: "user", Content: prompt}},
	})
}

// mode: single attempts the first target and no other. A second target that
// serves the same model is not a fallback — that is what distinguishes this
// mode from `fallback`, and it is why a model owned only by a later target is
// a 404 however many targets the config lists.
func TestStrategy_Single_UsesFirstTargetOnly(t *testing.T) {
	cfg := config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets: []config.Target{
			{VirtualKey: "only"},
			{VirtualKey: "spare"},
		},
	}

	t.Run("routes to the first target", func(t *testing.T) {
		resp, err := routeOnce(t, cfg, "hello",
			&miniStub{name: "only", models: []string{stratModel}},
			&miniStub{name: "spare", models: []string{stratModel}},
		)
		if err != nil {
			t.Fatalf("Route: %v", err)
		}
		if resp.Provider != "only" {
			t.Errorf("Provider = %q, want %q", resp.Provider, "only")
		}
	})

	t.Run("does not fall through to the second", func(t *testing.T) {
		var spareCalls atomic.Int64
		_, err := routeOnce(t, cfg, "hello",
			&miniStub{
				name:   "only",
				models: []string{stratModel},
				CompleteFunc: func(context.Context, core.Request) (*core.Response, error) {
					return nil, fmt.Errorf("provider API error (500): only down")
				},
			},
			&miniStub{
				name:   "spare",
				models: []string{stratModel},
				CompleteFunc: func(_ context.Context, req core.Request) (*core.Response, error) {
					spareCalls.Add(1)
					return okResponse("spare", req), nil
				},
			},
		)
		if err == nil {
			t.Fatal("expected an error: single must not fall through to the second target")
		}
		if spareCalls.Load() != 0 {
			t.Errorf("spare was called %d times; single must attempt one target", spareCalls.Load())
		}
	})
}

// mode: conditional matches on the model, which is the one thing a routing
// decision always has. A rule that matches sends its traffic to the named
// target; anything unmatched falls back to the first configured target.
func TestStrategy_Conditional_RoutesByModelRule(t *testing.T) {
	const specialModel = "premium-model-v1"

	cfg := config.Config{
		Strategy: config.StrategyConfig{
			Mode: config.ModeConditional,
			Conditions: []config.Condition{
				{Key: config.ConditionKeyModel, Value: specialModel, TargetKey: "special"},
			},
		},
		Targets: []config.Target{
			{VirtualKey: "general"},
			{VirtualKey: "special"},
		},
	}

	general := &miniStub{name: "general", models: []string{stratModel, specialModel}}
	special := &miniStub{name: "special", models: []string{stratModel, specialModel}}

	gw, err := testutil.NewTestGateway(t, cfg)
	if err != nil {
		t.Fatalf("aigateway.New: %v", err)
	}
	gw.RegisterProvider(general)
	gw.RegisterProvider(special)

	for _, tc := range []struct{ model, wantProvider string }{
		{specialModel, "special"},
		{stratModel, "general"},
	} {
		t.Run(tc.model, func(t *testing.T) {
			resp, err := gw.Route(t.Context(), core.Request{
				Model:    tc.model,
				Messages: []core.Message{{Role: "user", Content: "hello"}},
			})
			if err != nil {
				t.Fatalf("Route: %v", err)
			}
			if resp.Provider != tc.wantProvider {
				t.Errorf("model %q routed to %q, want %q", tc.model, resp.Provider, tc.wantProvider)
			}
		})
	}
}

// mode: conditional also matches a model prefix, which is how a family of model
// ids is pointed at one target without enumerating them.
func TestStrategy_Conditional_RoutesByModelPrefix(t *testing.T) {
	cfg := config.Config{
		Strategy: config.StrategyConfig{
			Mode: config.ModeConditional,
			Conditions: []config.Condition{
				{Key: config.ConditionKeyModelPrefix, Value: "str", TargetKey: "prefixed"},
			},
		},
		Targets: []config.Target{
			{VirtualKey: "general"},
			{VirtualKey: "prefixed"},
		},
	}

	resp, err := routeOnce(t, cfg, "hello",
		&miniStub{name: "general", models: []string{stratModel}},
		&miniStub{name: "prefixed", models: []string{stratModel}},
	)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if resp.Provider != "prefixed" {
		t.Errorf("Provider = %q, want %q", resp.Provider, "prefixed")
	}
}

// mode: content-based matches on the prompt. Its rules are evaluated in order
// and the first match wins; a request matching none goes to the first
// configured target.
func TestStrategy_ContentBased_RoutesByPrompt(t *testing.T) {
	cfg := config.Config{
		Strategy: config.StrategyConfig{
			Mode: config.ModeContentBased,
			ContentConditions: []config.ContentCondition{
				{Type: config.ContentConditionPromptContains, Value: "translate", TargetKey: "translator"},
				{Type: config.ContentConditionPromptRegex, Value: `^\s*func\b`, TargetKey: "coder"},
			},
		},
		Targets: []config.Target{
			{VirtualKey: "general"},
			{VirtualKey: "translator"},
			{VirtualKey: "coder"},
		},
	}

	for _, tc := range []struct{ name, prompt, wantProvider string }{
		{"substring rule", "please translate this", "translator"},
		{"regex rule", "func main() {}", "coder"},
		{"no rule matches", "hello there", "general"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := routeOnce(t, cfg, tc.prompt,
				&miniStub{name: "general", models: []string{stratModel}},
				&miniStub{name: "translator", models: []string{stratModel}},
				&miniStub{name: "coder", models: []string{stratModel}},
			)
			if err != nil {
				t.Fatalf("Route: %v", err)
			}
			if resp.Provider != tc.wantProvider {
				t.Errorf("prompt %q routed to %q, want %q", tc.prompt, resp.Provider, tc.wantProvider)
			}
		})
	}
}

// mode: cost-optimized prices candidates from the model catalog, so a model no
// catalog knows is unpriced by construction. unpriced_strategy decides what
// happens then, and the three values are the whole contract — the ordering of
// priced candidates is covered by the strategy's own unit tests.
func TestStrategy_CostOptimized_UnpricedStrategy(t *testing.T) {
	for _, tc := range []struct {
		unpriced  string
		wantError bool
	}{
		{config.UnpricedStrategyFallback, false},
		{config.UnpricedStrategyAllow, false},
		{config.UnpricedStrategySkip, true},
		{"", false}, // absent means fallback
	} {
		name := tc.unpriced
		if name == "" {
			name = "default"
		}
		t.Run(name, func(t *testing.T) {
			cfg := config.Config{
				Strategy: config.StrategyConfig{
					Mode:             config.ModeCostOptimized,
					UnpricedStrategy: tc.unpriced,
				},
				Targets: []config.Target{{VirtualKey: "cheap"}, {VirtualKey: "dear"}},
			}

			resp, err := routeOnce(t, cfg, "hello",
				&miniStub{name: "cheap", models: []string{stratModel}},
				&miniStub{name: "dear", models: []string{stratModel}},
			)
			if tc.wantError {
				if err == nil {
					t.Fatalf("expected an error: unpriced_strategy=skip must not route an unpriced model, got %q", resp.Provider)
				}
				return
			}
			if err != nil {
				t.Fatalf("Route: %v", err)
			}
			if resp.Provider == "" {
				t.Error("routed to no provider")
			}
		})
	}
}

// mode: ab-test splits traffic across labelled variants by weight. A zero
// weight means zero — that is what makes it usable to drain a variant before
// removing it — so all traffic must land on the other one.
func TestStrategy_ABTest_ZeroWeightVariantIsDrained(t *testing.T) {
	var liveCalls, drainedCalls atomic.Int64

	cfg := config.Config{
		Strategy: config.StrategyConfig{
			Mode: config.ModeABTest,
			ABVariants: []config.ABVariantConfig{
				{TargetKey: "live", Weight: 1, Label: "control"},
				{TargetKey: "drained", Weight: 0, Label: "challenger"},
			},
		},
		Targets: []config.Target{{VirtualKey: "live"}, {VirtualKey: "drained"}},
	}

	gw, err := testutil.NewTestGateway(t, cfg)
	if err != nil {
		t.Fatalf("aigateway.New: %v", err)
	}
	gw.RegisterProvider(countingStub("live", &liveCalls))
	gw.RegisterProvider(countingStub("drained", &drainedCalls))

	const n = 30
	for i := range n {
		if _, err := gw.Route(t.Context(), core.Request{
			Model:    stratModel,
			Messages: []core.Message{{Role: "user", Content: "req"}},
		}); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}

	if drainedCalls.Load() != 0 {
		t.Errorf("zero-weight variant received %d requests; zero means zero", drainedCalls.Load())
	}
	if liveCalls.Load() != n {
		t.Errorf("live variant handled %d of %d requests", liveCalls.Load(), n)
	}
}

// Both variants of a real split receive traffic. The assertion is deliberately
// weak (each gets at least one of 60 draws at a 50/50 split) so it measures
// that the draw runs at all rather than the quality of the RNG.
func TestStrategy_ABTest_SplitsAcrossVariants(t *testing.T) {
	var aCalls, bCalls atomic.Int64

	cfg := config.Config{
		Strategy: config.StrategyConfig{
			Mode: config.ModeABTest,
			ABVariants: []config.ABVariantConfig{
				{TargetKey: "variant-a", Weight: 50, Label: "control"},
				{TargetKey: "variant-b", Weight: 50, Label: "challenger"},
			},
		},
		Targets: []config.Target{{VirtualKey: "variant-a"}, {VirtualKey: "variant-b"}},
	}

	gw, err := testutil.NewTestGateway(t, cfg)
	if err != nil {
		t.Fatalf("aigateway.New: %v", err)
	}
	gw.RegisterProvider(countingStub("variant-a", &aCalls))
	gw.RegisterProvider(countingStub("variant-b", &bCalls))

	const n = 60
	for i := range n {
		if _, err := gw.Route(t.Context(), core.Request{
			Model:    stratModel,
			Messages: []core.Message{{Role: "user", Content: "req"}},
		}); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}

	a, b := aCalls.Load(), bCalls.Load()
	t.Logf("ab-test split: a=%d b=%d", a, b)
	if a+b != n {
		t.Errorf("total calls = %d, want %d", a+b, n)
	}
	if a == 0 || b == 0 {
		t.Errorf("split did not reach both variants: a=%d b=%d", a, b)
	}
}

func countingStub(name string, counter *atomic.Int64) *miniStub {
	return &miniStub{
		name:   name,
		models: []string{stratModel},
		CompleteFunc: func(_ context.Context, req core.Request) (*core.Response, error) {
			counter.Add(1)
			return okResponse(name, req), nil
		},
	}
}

func okResponse(name string, req core.Request) *core.Response {
	return &core.Response{
		ID:       name + "-resp",
		Object:   "chat.completion",
		Model:    req.Model,
		Provider: name,
		Choices: []core.Choice{
			{Message: core.Message{Role: "assistant", Content: "ok from " + name}, FinishReason: "stop"},
		},
	}
}
