package aigateway

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/providers"
)

func TestGateway_RouteStream_ContentBasedPromptRegex(t *testing.T) {
	gw, err := newTestGateway(t, Config{
		Strategy: StrategyConfig{
			Mode: ModeContentBased,
			ContentConditions: []ContentCondition{{
				Type:      "prompt_regex",
				Value:     `(?i)\b(code|function)\b`,
				TargetKey: "code-stream",
			}},
		},
		Targets: []Target{
			{VirtualKey: "general-stream"},
			{VirtualKey: "code-stream"},
		},
	})

	if err != nil {
		t.Fatal(err)
	}
	if len(gw.streamingContent) != 1 || gw.streamingContent[0].re == nil {
		t.Fatal("expected compiled streaming content regex")
	}

	selected := make(chan string, 2)
	recordStream := func(name string) func(context.Context, providers.Request) (<-chan providers.StreamChunk, error) {
		return func(context.Context, providers.Request) (<-chan providers.StreamChunk, error) {
			selected <- name
			ch := make(chan providers.StreamChunk)
			close(ch)
			return ch, nil
		}
	}
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{
			name:   "general-stream",
			models: []string{"gpt-4o"},
		},
		streamFn: recordStream("general-stream"),
	})
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{
			name:   "code-stream",
			models: []string{"gpt-4o"},
		},
		streamFn: recordStream("code-stream"),
	})

	out, err := gw.RouteStream(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Stream:   true,
		Messages: []providers.Message{{Role: "user", Content: "write a Go function"}},
	})
	if err != nil {
		t.Fatalf("RouteStream: %v", err)
	}
	for range out { //nolint:revive // empty-block: intentionally draining the stream to completion
	}

	select {
	case got := <-selected:
		if got != "code-stream" {
			t.Fatalf("selected provider = %q, want code-stream", got)
		}
	default:
		t.Fatal("stream provider was not selected")
	}
}

func TestGateway_NewRejectsInvalidStreamingPromptRegex(t *testing.T) {
	_, err := newTestGateway(t, Config{
		Strategy: StrategyConfig{
			Mode: ModeContentBased,
			ContentConditions: []ContentCondition{{
				Type:      "prompt_regex",
				Value:     `[invalid`,
				TargetKey: "code-stream",
			}},
		},
		Targets: []Target{{VirtualKey: "code-stream"}},
	})

	if err == nil {
		t.Fatal("expected invalid regex error")
	}
	if !strings.Contains(err.Error(), "invalid regex") {
		t.Fatalf("error = %v, want invalid regex", err)
	}
}

func TestGateway_ReloadConfigRejectsInvalidStreamingPromptRegex(t *testing.T) {
	gw, err := newTestGateway(t, Config{
		Strategy: StrategyConfig{
			Mode: ModeContentBased,
			ContentConditions: []ContentCondition{{
				Type:      "prompt_regex",
				Value:     `docs`,
				TargetKey: "general-stream",
			}},
		},
		Targets: []Target{{VirtualKey: "general-stream"}},
	})

	if err != nil {
		t.Fatal(err)
	}

	err = gw.ReloadConfig(context.Background(), Config{
		Strategy: StrategyConfig{
			Mode: ModeContentBased,
			ContentConditions: []ContentCondition{{
				Type:      "prompt_regex",
				Value:     `[invalid`,
				TargetKey: "general-stream",
			}},
		},
		Targets: []Target{{VirtualKey: "general-stream"}},
	})
	if err == nil {
		t.Fatal("expected invalid regex error")
	}
	if !strings.Contains(err.Error(), "invalid regex") {
		t.Fatalf("error = %v, want invalid regex", err)
	}
	if len(gw.streamingContent) != 1 {
		t.Fatalf("streaming content = %d, want previous config to remain", len(gw.streamingContent))
	}
	if gw.streamingContent[0].Value != "docs" || gw.streamingContent[0].re == nil {
		t.Fatalf("streaming content was replaced after invalid reload: %#v", gw.streamingContent[0])
	}
}

func TestGateway_ReloadConfigRebuildsStreamingContentRegex(t *testing.T) {
	gw, err := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "general-stream"}},
	})

	if err != nil {
		t.Fatal(err)
	}
	if len(gw.streamingContent) != 0 {
		t.Fatalf("single strategy streaming content = %d, want 0", len(gw.streamingContent))
	}

	err = gw.ReloadConfig(context.Background(), Config{
		Strategy: StrategyConfig{
			Mode: ModeContentBased,
			ContentConditions: []ContentCondition{
				{Type: "prompt_contains", Value: "docs", TargetKey: "general-stream"},
				{Type: "prompt_regex", Value: `(?i)\b(code|function)\b`, TargetKey: "code-stream"},
			},
		},
		Targets: []Target{
			{VirtualKey: "general-stream"},
			{VirtualKey: "code-stream"},
		},
	})
	if err != nil {
		t.Fatalf("ReloadConfig: %v", err)
	}
	if len(gw.streamingContent) != 2 {
		t.Fatalf("streaming content = %d, want 2", len(gw.streamingContent))
	}
	if gw.streamingContent[0].re != nil {
		t.Fatal("prompt_contains rule should not have a compiled regex")
	}
	if gw.streamingContent[1].re == nil {
		t.Fatal("prompt_regex rule should have a compiled regex")
	}
}

func TestGateway_RouteStream_LeastLatencyUsesObservedP50(t *testing.T) {
	gw, err := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeLatency},
		Targets: []Target{
			{VirtualKey: "slow"},
			{VirtualKey: "fast"},
		},
	})

	if err != nil {
		t.Fatal(err)
	}
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: "slow", models: []string{"gpt-4o"}},
		streamErr:    errors.New("slow provider selected"),
	})
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: "fast", models: []string{"gpt-4o"}},
	})
	gw.latencyTracker.Record("slow", 120*time.Millisecond)
	gw.latencyTracker.Record("fast", 10*time.Millisecond)

	ch, err := gw.RouteStream(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("RouteStream error = %v, want fast provider", err)
	}
	drainStream(t, ch)
}

func TestGateway_RouteStream_LeastLatencyRecordsStreamLatency(t *testing.T) {
	gw, err := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeLatency},
		Targets:  []Target{{VirtualKey: "stream"}},
	})

	if err != nil {
		t.Fatal(err)
	}
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: "stream", models: []string{"gpt-4o"}},
	})

	ch, err := gw.RouteStream(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("RouteStream error = %v", err)
	}
	drainStream(t, ch)

	if !gw.latencyTracker.HasSamples("stream") {
		t.Fatal("expected RouteStream to record a latency sample")
	}
}

func TestGateway_RouteStream_CostOptimizedUsesCatalogCost(t *testing.T) {
	gw, err := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeCostOptimized},
		Targets: []Target{
			{VirtualKey: "expensive"},
			{VirtualKey: "cheap"},
		},
	})

	if err != nil {
		t.Fatal(err)
	}
	gw.catalog = models.Catalog{
		"expensive/gpt-4o": {
			Provider: "expensive",
			ModelID:  "gpt-4o",
			Mode:     models.ModeChat,
			Pricing: models.Pricing{
				InputPerMTokens: ptrFloat64(10),
			},
		},
		"cheap/gpt-4o": {
			Provider: "cheap",
			ModelID:  "gpt-4o",
			Mode:     models.ModeChat,
			Pricing: models.Pricing{
				InputPerMTokens: ptrFloat64(1),
			},
		},
	}
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: "expensive", models: []string{"gpt-4o"}},
		streamErr:    errors.New("expensive provider selected"),
	})
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: "cheap", models: []string{"gpt-4o"}},
	})

	ch, err := gw.RouteStream(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hello world"}},
	})
	if err != nil {
		t.Fatalf("RouteStream error = %v, want cheap provider", err)
	}
	drainStream(t, ch)
}

func TestGateway_RouteStream_CostOptimizedSkipErrorsWhenAllStreamCandidatesUnpriced(t *testing.T) {
	gw, err := newTestGateway(t, Config{
		Strategy: StrategyConfig{
			Mode:             ModeCostOptimized,
			UnpricedStrategy: unpricedStrategySkip,
		},
		Targets: []Target{
			{VirtualKey: "first"},
			{VirtualKey: "second"},
		},
	})

	if err != nil {
		t.Fatal(err)
	}
	gw.catalog = models.Catalog{
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
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: "first", models: []string{"gpt-4o"}},
		streamErr:    errors.New("first provider selected"),
	})
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: "second", models: []string{"gpt-4o"}},
		streamErr:    errors.New("second provider selected"),
	})

	_, err = gw.RouteStream(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hello"}},
	})
	if err == nil {
		t.Fatal("expected RouteStream to reject unpriced providers in skip mode")
	}
	if !strings.Contains(err.Error(), "no priced provider supports model gpt-4o") {
		t.Fatalf("RouteStream error = %v, want no priced provider error", err)
	}
}

func TestGateway_StreamingCostOrderHandlesUnpricedStrategies(t *testing.T) {
	tests := []struct {
		name             string
		unpricedStrategy string
		catalog          models.Catalog
		want             []string
	}{
		{
			name:             "skip puts priced providers first",
			unpricedStrategy: unpricedStrategySkip,
			catalog: models.Catalog{
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
						InputPerMTokens: ptrFloat64(1),
					},
				},
			},
			want: []string{"priced", "unpriced", "missing", "plain"},
		},
		{
			name:             "allow keeps model-found unpriced providers eligible",
			unpricedStrategy: unpricedStrategyAllow,
			catalog: models.Catalog{
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
						InputPerMTokens: ptrFloat64(1),
					},
				},
			},
			want: []string{"unpriced", "priced", "missing", "plain"},
		},
		{
			name: "fallback returns target order when nothing is priced",
			catalog: models.Catalog{
				"unpriced/gpt-4o": {
					Provider: "unpriced",
					ModelID:  "gpt-4o",
					Mode:     models.ModeChat,
					Pricing:  models.Pricing{},
				},
			},
			want: []string{"unpriced", "priced", "missing", "plain"},
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
					{VirtualKey: "missing"},
					{VirtualKey: "plain"},
				},
			})

			if err != nil {
				t.Fatal(err)
			}
			gw.catalog = tt.catalog
			gw.RegisterProvider(&mockStreamProvider{
				mockProvider: mockProvider{name: "unpriced", models: []string{"gpt-4o"}},
			})
			gw.RegisterProvider(&mockStreamProvider{
				mockProvider: mockProvider{name: "priced", models: []string{"gpt-4o"}},
			})
			gw.RegisterProvider(&mockStreamProvider{
				mockProvider: mockProvider{name: "missing", models: []string{"gpt-4o"}},
			})
			gw.RegisterProvider(&mockProvider{
				name:   "plain",
				models: []string{"gpt-4o"},
			})

			got := streamTargetOrder(t, gw, providers.Request{
				Model:    "gpt-4o",
				Messages: []providers.Message{{Role: "user", Content: "hello world"}},
			})
			requireKeys(t, got, tt.want...)
		})
	}
}

func TestGateway_StreamingLatencyOrderFallsBackWithoutStreamingCandidates(t *testing.T) {
	gw, err := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeLatency},
		Targets: []Target{
			{VirtualKey: "plain"},
			{VirtualKey: "unsupported"},
			{VirtualKey: "missing"},
		},
	})

	if err != nil {
		t.Fatal(err)
	}
	gw.RegisterProvider(&mockProvider{
		name:   "plain",
		models: []string{"gpt-4o"},
	})
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: "unsupported", models: []string{"other-model"}},
	})

	got := streamTargetOrder(t, gw, providers.Request{Model: "gpt-4o"})
	requireKeys(t, got, "plain", "unsupported", "missing")
}

func TestGateway_StreamingLatencyOrderTriesUnseenBeforeSampled(t *testing.T) {
	gw, err := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeLatency},
		Targets: []Target{
			{VirtualKey: "unseen-a"},
			{VirtualKey: "sampled"},
			{VirtualKey: "unseen-b"},
		},
	})

	if err != nil {
		t.Fatal(err)
	}
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: "unseen-a", models: []string{"gpt-4o"}},
	})
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: "sampled", models: []string{"gpt-4o"}},
	})
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: "unseen-b", models: []string{"gpt-4o"}},
	})
	gw.latencyTracker.Record("sampled", 10*time.Millisecond)

	got := streamTargetOrder(t, gw, providers.Request{Model: "gpt-4o"})
	if got[2] != "sampled" {
		t.Fatalf("got keys %v, want sampled provider after unseen providers", got)
	}
	firstTwoUnseen := (got[0] == "unseen-a" && got[1] == "unseen-b") ||
		(got[0] == "unseen-b" && got[1] == "unseen-a")
	if !firstTwoUnseen {
		t.Fatalf("got keys %v, want unseen providers first", got)
	}
}

func TestGateway_StreamingCostOrderFallsBackWithoutStreamingCandidates(t *testing.T) {
	gw, err := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeCostOptimized},
		Targets: []Target{
			{VirtualKey: "plain"},
			{VirtualKey: "unsupported"},
			{VirtualKey: "missing"},
		},
	})

	if err != nil {
		t.Fatal(err)
	}
	gw.RegisterProvider(&mockProvider{
		name:   "plain",
		models: []string{"gpt-4o"},
	})
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: "unsupported", models: []string{"other-model"}},
	})

	got := streamTargetOrder(t, gw, providers.Request{Model: "gpt-4o"})
	requireKeys(t, got, "plain", "unsupported", "missing")
}
