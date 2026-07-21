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

func TestGateway_RouteStream_RetriesSynchronousStartBeforeFallback(t *testing.T) {
	firstCalls := 0
	secondCalls := 0
	gw, err := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeFallback},
		Targets: []Target{
			{VirtualKey: "first", Retry: &RetryConfig{Attempts: 2, InitialBackoffMs: 1}},
			{VirtualKey: "second"},
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: "first", models: []string{"gpt-4o"}},
		streamFn: func(context.Context, providers.Request) (<-chan providers.StreamChunk, error) {
			firstCalls++
			if firstCalls == 1 {
				return nil, errors.New("transient start failure")
			}
			ch := make(chan providers.StreamChunk)
			close(ch)
			return ch, nil
		},
	})
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: "second", models: []string{"gpt-4o"}},
		streamFn: func(context.Context, providers.Request) (<-chan providers.StreamChunk, error) {
			secondCalls++
			ch := make(chan providers.StreamChunk)
			close(ch)
			return ch, nil
		},
	})

	ch, err := gw.RouteStream(context.Background(), streamTestRequest())
	if err != nil {
		t.Fatalf("RouteStream: %v", err)
	}
	drainMeteredStream(t, ch)
	if firstCalls != 2 || secondCalls != 0 {
		t.Fatalf("start calls first=%d second=%d, want 2/0", firstCalls, secondCalls)
	}
}

func TestGateway_RouteStream_DoesNotReplayAfterChannelIsVisible(t *testing.T) {
	secondCalls := 0
	gw, err := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeFallback},
		Targets:  []Target{{VirtualKey: "first"}, {VirtualKey: "second"}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: "first", models: []string{"gpt-4o"}},
		streamFn: func(context.Context, providers.Request) (<-chan providers.StreamChunk, error) {
			ch := make(chan providers.StreamChunk, 2)
			ch <- providers.StreamChunk{Choices: []providers.StreamChoice{{Delta: providers.MessageDelta{Content: "visible"}}}}
			ch <- providers.StreamChunk{Error: errors.New("midstream failure")}
			close(ch)
			return ch, nil
		},
	})
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: "second", models: []string{"gpt-4o"}},
		streamFn: func(context.Context, providers.Request) (<-chan providers.StreamChunk, error) {
			secondCalls++
			ch := make(chan providers.StreamChunk)
			close(ch)
			return ch, nil
		},
	})

	ch, err := gw.RouteStream(context.Background(), streamTestRequest())
	if err != nil {
		t.Fatalf("RouteStream: %v", err)
	}
	drainMeteredStream(t, ch)
	if secondCalls != 0 {
		t.Fatalf("fallback replayed after client-visible stream start: second calls=%d", secondCalls)
	}
}

// TestGateway_RouteStream_HangingStartAbandonedAtRequestTimeout proves the
// start/retry phase is bounded by Config.RequestTimeout: a target configured
// with several retry attempts against a provider that never answers must not
// hold RouteStream open for anywhere near the full retry/backoff window (it
// would previously block until the caller's own context was cancelled).
func TestGateway_RouteStream_HangingStartAbandonedAtRequestTimeout(t *testing.T) {
	gw, err := newTestGateway(t, Config{
		RequestTimeout: "50ms",
		Strategy:       StrategyConfig{Mode: ModeFallback},
		Targets: []Target{
			{VirtualKey: "hangs", Retry: &RetryConfig{Attempts: 5, InitialBackoffMs: 1}},
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: "hangs", models: []string{"gpt-4o"}},
		streamFn: func(ctx context.Context, _ providers.Request) (<-chan providers.StreamChunk, error) {
			<-ctx.Done() // simulate a provider that never answers on its own
			return nil, ctx.Err()
		},
	})

	// A generous bound so the test itself terminates even if the fix regresses;
	// the assertion below is what actually proves the abandonment is prompt.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	_, err = gw.RouteStream(ctx, streamTestRequest())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected RouteStream to fail once the hanging start is abandoned")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RouteStream error = %v, want a context-deadline error from the start-phase timeout", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("RouteStream took %s to abandon a hanging start with 5 configured retry attempts; want near the 50ms request timeout, not the full retry/backoff window", elapsed)
	}
}

// TestGateway_RouteStream_LiveStreamNotKilledByStartPhaseDeadline is the
// regression guard for the naive fix: RequestTimeout must bound only stream
// START, never a stream already visible to the caller. A provider that
// starts immediately but keeps sending well past RequestTimeout must drain
// without error.
func TestGateway_RouteStream_LiveStreamNotKilledByStartPhaseDeadline(t *testing.T) {
	gw, err := newTestGateway(t, Config{
		RequestTimeout: "20ms",
		Strategy:       StrategyConfig{Mode: ModeSingle},
		Targets:        []Target{{VirtualKey: "slow-body"}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: "slow-body", models: []string{"gpt-4o"}},
		streamFn: func(ctx context.Context, _ providers.Request) (<-chan providers.StreamChunk, error) {
			ch := make(chan providers.StreamChunk, 1)
			go func() {
				defer close(ch)
				// Outlives RequestTimeout before sending its only chunk. If the
				// start-phase deadline leaked into this context instead of being
				// released once the channel became visible, ctx would already be
				// done by the time we get here.
				time.Sleep(100 * time.Millisecond)
				if ctx.Err() != nil {
					ch <- providers.StreamChunk{Error: errors.New("stream context was cancelled by the start-phase deadline")}
					return
				}
				ch <- providers.StreamChunk{Choices: []providers.StreamChoice{{Delta: providers.MessageDelta{Content: "still streaming"}}}}
			}()
			return ch, nil
		},
	})

	ch, err := gw.RouteStream(context.Background(), streamTestRequest())
	if err != nil {
		t.Fatalf("RouteStream: %v", err)
	}
	drainStream(t, ch)
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
