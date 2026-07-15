package aigateway

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/providers"
)

// TestGateway_RouteStreamMatchesRouteTargetOrder asserts Route (Strategy.Execute) and
// RouteStream (Strategy.SelectTargets) resolve consistently per strategy: for
// deterministic strategies both pick the same first target and SelectTargets
// exposes every configured target; for weighted-random strategies both pick
// within the configured target set from the one shared selection implementation.
func TestGateway_RouteStreamMatchesRouteTargetOrder(t *testing.T) {
	req := providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "please write code"}},
	}

	tests := []struct {
		name          string
		strategy      StrategyConfig
		targets       []Target
		deterministic bool
		wantFirst     string
		// exposesAllTargets is false only for single, which intentionally offers
		// no fallbacks — every other strategy appends the remaining targets.
		exposesAllTargets bool
	}{
		{
			name:          "single",
			strategy:      StrategyConfig{Mode: ModeSingle},
			targets:       []Target{{VirtualKey: "a"}, {VirtualKey: "b"}},
			deterministic: true,
			wantFirst:     "a",
		},
		{
			name:              "fallback",
			strategy:          StrategyConfig{Mode: ModeFallback},
			targets:           []Target{{VirtualKey: "a"}, {VirtualKey: "b"}, {VirtualKey: "c"}},
			deterministic:     true,
			wantFirst:         "a",
			exposesAllTargets: true,
		},
		{
			name:              "conditional",
			strategy:          StrategyConfig{Mode: ModeConditional, Conditions: []Condition{{Key: "model", Value: "gpt-4o", TargetKey: "b"}}},
			targets:           []Target{{VirtualKey: "a"}, {VirtualKey: "b"}},
			deterministic:     true,
			wantFirst:         "b",
			exposesAllTargets: true,
		},
		{
			name:              "content-based",
			strategy:          StrategyConfig{Mode: ModeContentBased, ContentConditions: []ContentCondition{{Type: "prompt_contains", Value: "code", TargetKey: "b"}}},
			targets:           []Target{{VirtualKey: "a"}, {VirtualKey: "b"}},
			deterministic:     true,
			wantFirst:         "b",
			exposesAllTargets: true,
		},
		{
			name:              "load-balance",
			strategy:          StrategyConfig{Mode: ModeLoadBalance},
			targets:           []Target{{VirtualKey: "a", Weight: 1}, {VirtualKey: "b", Weight: 1}},
			exposesAllTargets: true,
		},
		{
			name:              "ab-test",
			strategy:          StrategyConfig{Mode: ModeABTest, ABVariants: []ABVariantConfig{{TargetKey: "a", Weight: 1, Label: "control"}, {TargetKey: "b", Weight: 1, Label: "challenger"}}},
			targets:           []Target{{VirtualKey: "a"}, {VirtualKey: "b"}},
			exposesAllTargets: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gw, err := newTestGateway(t, Config{Strategy: tt.strategy, Targets: tt.targets})
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			var served string
			for _, tgt := range tt.targets {
				name := tgt.VirtualKey
				gw.RegisterProvider(&mockStreamProvider{
					mockProvider: mockProvider{name: name, models: []string{"gpt-4o"}, resp: &providers.Response{ID: name, Model: "gpt-4o"}},
					streamFn: func(context.Context, providers.Request) (<-chan providers.StreamChunk, error) {
						served = name
						ch := make(chan providers.StreamChunk)
						close(ch)
						return ch, nil
					},
				})
			}

			order := streamTargetOrder(t, gw, req)
			if tt.exposesAllTargets {
				assertSameTargetSet(t, order, tt.targets)
			}

			resp, err := gw.Route(context.Background(), req)
			if err != nil {
				t.Fatalf("Route: %v", err)
			}
			ch, err := gw.RouteStream(context.Background(), req)
			if err != nil {
				t.Fatalf("RouteStream: %v", err)
			}
			drainStream(t, ch)

			if tt.deterministic {
				if order[0] != tt.wantFirst {
					t.Fatalf("SelectTargets first = %q, want %q", order[0], tt.wantFirst)
				}
				if resp.Provider != tt.wantFirst {
					t.Fatalf("Route provider = %q, want %q", resp.Provider, tt.wantFirst)
				}
				if served != tt.wantFirst {
					t.Fatalf("RouteStream served %q, want %q", served, tt.wantFirst)
				}
				return
			}
			assertInTargets(t, resp.Provider, tt.targets)
			assertInTargets(t, served, tt.targets)
		})
	}
}

func TestGateway_Route_Single(t *testing.T) {
	gw, _ := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: mockProviderName}},
	})

	gw.RegisterProvider(&mockProvider{
		name:   mockProviderName,
		models: []string{"gpt-4o"},
		resp:   &providers.Response{ID: "r1", Model: "gpt-4o"},
	})

	resp, err := gw.Route(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ID != "r1" {
		t.Errorf("got ID %q, want r1", resp.ID)
	}
}

func TestGateway_Route_NormalizesCompletionTokenLimitsBeforePlugins(t *testing.T) {
	gw, _ := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: mockProviderName}},
		Plugins: []PluginConfig{
			{
				Name:    "max-token",
				Enabled: true,
				Stage:   "before_request",
				Config:  map[string]any{"max_tokens": 100},
			},
		},
	})

	gw.RegisterProvider(&mockProvider{
		name:   mockProviderName,
		models: []string{"gpt-4o"},
		resp:   &providers.Response{ID: "r1", Model: "gpt-4o"},
	})
	if err := gw.LoadPlugins(); err != nil {
		t.Fatalf("LoadPlugins failed: %v", err)
	}

	maxCompletionTokens := 200
	_, err := gw.Route(context.Background(), providers.Request{
		Model:               "gpt-4o",
		Messages:            []providers.Message{{Role: "user", Content: "hi"}},
		MaxCompletionTokens: &maxCompletionTokens,
	})
	if err == nil {
		t.Fatal("expected max-token plugin rejection")
	}
	if !strings.Contains(err.Error(), "max_tokens 200 exceeds limit of 100") {
		t.Fatalf("error = %q, want max-token rejection with normalized max_tokens", err.Error())
	}
}

func TestGateway_Route_NormalizesCompletionTokenLimitsBeforeProvider(t *testing.T) {
	var captured providers.Request
	gw, _ := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: mockProviderName}},
	})

	gw.RegisterProvider(&mockProvider{
		name:   mockProviderName,
		models: []string{"gpt-4o"},
		completeFn: func(_ context.Context, req providers.Request) (*providers.Response, error) {
			captured = req
			return &providers.Response{ID: "r1", Model: req.Model}, nil
		},
	})

	maxCompletionTokens := 17
	resp, err := gw.Route(context.Background(), providers.Request{
		Model:               "gpt-4o",
		Messages:            []providers.Message{{Role: "user", Content: "hi"}},
		MaxCompletionTokens: &maxCompletionTokens,
	})
	if err != nil {
		t.Fatalf("Route returned error: %v", err)
	}
	if resp.ID != "r1" {
		t.Fatalf("response ID = %q, want r1", resp.ID)
	}
	if captured.MaxTokens == nil || *captured.MaxTokens != maxCompletionTokens {
		t.Fatalf("captured MaxTokens = %v, want %d", captured.MaxTokens, maxCompletionTokens)
	}
	if captured.MaxCompletionTokens == nil || *captured.MaxCompletionTokens != maxCompletionTokens {
		t.Fatalf("captured MaxCompletionTokens = %v, want preserved %d", captured.MaxCompletionTokens, maxCompletionTokens)
	}
}

func TestGateway_Route_Fallback(t *testing.T) {
	gw, _ := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeFallback},
		Targets: []Target{
			{VirtualKey: "bad"},
			{VirtualKey: "good"},
		},
	})

	gw.RegisterProvider(&mockProvider{
		name:   "bad",
		models: []string{"gpt-4o"},
		err:    fmt.Errorf("provider down"),
	})
	gw.RegisterProvider(&mockProvider{
		name:   "good",
		models: []string{"gpt-4o"},
		resp:   &providers.Response{ID: "fallback-ok"},
	})

	resp, err := gw.Route(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ID != "fallback-ok" {
		t.Errorf("got ID %q, want fallback-ok", resp.ID)
	}
}

func TestGateway_Route_CostOptimizedPassesUnpricedStrategy(t *testing.T) {
	tests := []struct {
		name             string
		unpricedStrategy string
		wantProvider     string
	}{
		{
			name:             "skip routes to priced provider",
			unpricedStrategy: unpricedStrategySkip,
			wantProvider:     "priced",
		},
		{
			name:             "allow routes to unpriced provider",
			unpricedStrategy: unpricedStrategyAllow,
			wantProvider:     "unpriced",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gw, err := newTestGateway(t, Config{
				Strategy: StrategyConfig{
					Mode:             ModeCostOptimized,
					UnpricedStrategy: tt.unpricedStrategy,
				},
				Targets: []Target{
					{VirtualKey: "unpriced"},
					{VirtualKey: "priced"},
				},
			})

			if err != nil {
				t.Fatalf("New: %v", err)
			}
			gw.catalog = models.Catalog{
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
						InputPerMTokens: ptrFloat64(1.0),
					},
				},
			}
			gw.RegisterProvider(&mockProvider{
				name:   "unpriced",
				models: []string{"gpt-4o"},
				resp:   &providers.Response{ID: "unpriced", Model: "gpt-4o"},
			})
			gw.RegisterProvider(&mockProvider{
				name:   "priced",
				models: []string{"gpt-4o"},
				resp:   &providers.Response{ID: "priced", Model: "gpt-4o"},
			})

			resp, err := gw.Route(context.Background(), providers.Request{
				Model:    "gpt-4o",
				Messages: []providers.Message{{Role: "user", Content: "hello"}},
			})
			if err != nil {
				t.Fatalf("Route: %v", err)
			}
			if resp.Provider != tt.wantProvider {
				t.Fatalf("got provider %q, want %q", resp.Provider, tt.wantProvider)
			}
			if resp.ID != tt.wantProvider {
				t.Fatalf("got response ID %q, want %q", resp.ID, tt.wantProvider)
			}
		})
	}
}

func TestGateway_Route_NoTargets(t *testing.T) {
	// After #256, New() validates the config eagerly, so the "no targets"
	// error is now returned at construction time rather than at route time.
	_, err := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
	})

	if err == nil {
		t.Fatal("expected error for no targets")
	}
}
