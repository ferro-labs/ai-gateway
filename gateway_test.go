package aigateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/circuitbreaker"
	"github.com/ferro-labs/ai-gateway/internal/events"
	"github.com/ferro-labs/ai-gateway/internal/logging"
	"github.com/ferro-labs/ai-gateway/internal/metrics"
	cacheplugin "github.com/ferro-labs/ai-gateway/internal/plugins/cache"
	"github.com/ferro-labs/ai-gateway/mcp"
	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	// Register built-in plugins so benchmark helpers can load them via LoadPlugins.
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/maxtoken"
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/wordfilter"
)

// mockProviderName is the canonical provider name used by test mock providers.
const mockProviderName = "mock"

// mockProvider is a test double for providers.Provider.
type mockProvider struct {
	name       string
	models     []string
	resp       *providers.Response
	err        error
	completeFn func(context.Context, providers.Request) (*providers.Response, error)
}

func (m *mockProvider) Name() string                  { return m.name }
func (m *mockProvider) SupportedModels() []string     { return m.models }
func (m *mockProvider) Models() []providers.ModelInfo { return nil }
func (m *mockProvider) SupportsModel(model string) bool {
	for _, mm := range m.models {
		if mm == model {
			return true
		}
	}
	return false
}
func (m *mockProvider) Complete(ctx context.Context, req providers.Request) (*providers.Response, error) {
	if m.completeFn != nil {
		return m.completeFn(ctx, req)
	}
	return m.resp, m.err
}

type mockStreamProvider struct {
	mockProvider
	streamErr error
	streamCh  <-chan providers.StreamChunk
	streamFn  func(context.Context, providers.Request) (<-chan providers.StreamChunk, error)
}

func (m *mockStreamProvider) CompleteStream(ctx context.Context, req providers.Request) (<-chan providers.StreamChunk, error) {
	if m.streamErr != nil {
		return nil, m.streamErr
	}
	if m.streamFn != nil {
		return m.streamFn(ctx, req)
	}
	if m.streamCh != nil {
		return m.streamCh, nil
	}
	// Default: return an already-closed channel so streamwrap.Meter drains immediately.
	ch := make(chan providers.StreamChunk)
	close(ch)
	return ch, nil
}

func counterValue(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	m := &dto.Metric{}
	if err := c.Write(m); err != nil {
		t.Fatalf("failed to read counter value: %v", err)
	}
	return m.GetCounter().GetValue()
}

func ptrFloat64(v float64) *float64 { return &v }

func drainStream(t *testing.T, ch <-chan providers.StreamChunk) {
	t.Helper()
	for chunk := range ch {
		if chunk.Error != nil {
			t.Fatalf("stream chunk error: %v", chunk.Error)
		}
	}
}

func requireKeys(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got keys %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got keys %v, want %v", got, want)
		}
	}
}

func TestGateway_Route_Single(t *testing.T) {
	gw, _ := New(Config{
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
	gw, _ := New(Config{
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
	gw, _ := New(Config{
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
	gw, _ := New(Config{
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
			gw, err := New(Config{
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
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
	})

	_, err := gw.Route(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error for no targets")
	}
}

func TestGateway_RouteStream_ContentBasedPromptRegex(t *testing.T) {
	gw, err := New(Config{
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
	for range out { //nolint:revive
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
	_, err := New(Config{
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
	gw, err := New(Config{
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

	err = gw.ReloadConfig(Config{
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
	gw, err := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "general-stream"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(gw.streamingContent) != 0 {
		t.Fatalf("single strategy streaming content = %d, want 0", len(gw.streamingContent))
	}

	err = gw.ReloadConfig(Config{
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

func TestStreamingContentConditionMatchesPromptRegexRequiresCompiledRegex(t *testing.T) {
	req := providers.Request{
		Messages: []providers.Message{{Role: "user", Content: "write docs"}},
	}

	if streamingContentConditionMatches(streamingContentCondition{
		ContentCondition: ContentCondition{Type: "prompt_regex", Value: "docs"},
	}, req) {
		t.Fatal("uncompiled prompt_regex should not match")
	}

	compiled, err := compileStreamingContentConditions(ModeContentBased, []ContentCondition{{
		Type:      "prompt_regex",
		Value:     "docs",
		TargetKey: "general-stream",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !streamingContentConditionMatches(compiled[0], req) {
		t.Fatal("compiled prompt_regex should match")
	}
	if streamingContentConditionMatches(streamingContentCondition{
		ContentCondition: ContentCondition{Type: "unknown", Value: "docs"},
	}, req) {
		t.Fatal("unknown streaming content condition should not match")
	}
}

func TestGateway_RouteStream_LeastLatencyUsesObservedP50(t *testing.T) {
	gw, err := New(Config{
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
	gw, err := New(Config{
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
	gw, err := New(Config{
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
	gw, err := New(Config{
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
			gw, err := New(Config{
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

			got, err := gw.streamingCostOrderLocked(gw.config.Targets, providers.Request{
				Model:    "gpt-4o",
				Messages: []providers.Message{{Role: "user", Content: "hello world"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			requireKeys(t, got, tt.want...)
		})
	}
}

func TestGateway_StreamingLatencyOrderFallsBackWithoutStreamingCandidates(t *testing.T) {
	gw, err := New(Config{
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

	got := gw.streamingLatencyOrderLocked(gw.config.Targets, providers.Request{Model: "gpt-4o"})
	requireKeys(t, got, "plain", "unsupported", "missing")
}

func TestGateway_StreamingLatencyOrderTriesUnseenBeforeSampled(t *testing.T) {
	gw, err := New(Config{
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

	got := gw.streamingLatencyOrderLocked(gw.config.Targets, providers.Request{Model: "gpt-4o"})
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
	gw, err := New(Config{
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

	got, err := gw.streamingCostOrderLocked(gw.config.Targets, providers.Request{Model: "gpt-4o"})
	if err != nil {
		t.Fatal(err)
	}
	requireKeys(t, got, "plain", "unsupported", "missing")
}

func TestGateway_RouteStream_ImmediateFailure_IncrementsProviderErrors(t *testing.T) {
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "mock-stream"}},
	})
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{
			name:   "mock-stream",
			models: []string{"gpt-4o"},
		},
		streamErr: errors.New("stream failed"),
	})

	beforeReq := counterValue(t, metrics.RequestsTotal.WithLabelValues("mock-stream", "gpt-4o", "error"))
	beforeProvErr := counterValue(t, metrics.ProviderErrors.WithLabelValues("mock-stream", "provider_error"))

	_, err := gw.RouteStream(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected stream startup error")
	}

	afterReq := counterValue(t, metrics.RequestsTotal.WithLabelValues("mock-stream", "gpt-4o", "error"))
	afterProvErr := counterValue(t, metrics.ProviderErrors.WithLabelValues("mock-stream", "provider_error"))
	if afterReq-beforeReq != 1 {
		t.Fatalf("gateway_requests_total error delta = %v, want 1", afterReq-beforeReq)
	}
	if afterProvErr-beforeProvErr != 1 {
		t.Fatalf("gateway_provider_errors_total provider_error delta = %v, want 1", afterProvErr-beforeProvErr)
	}
}

func TestGateway_RouteStream_ImmediateCircuitOpen_IncrementsCircuitOpenProviderErrors(t *testing.T) {
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "mock-stream"}},
	})
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{
			name:   "mock-stream",
			models: []string{"gpt-4o"},
		},
		streamErr: circuitbreaker.ErrCircuitOpen,
	})

	beforeReq := counterValue(t, metrics.RequestsTotal.WithLabelValues("mock-stream", "gpt-4o", "error"))
	beforeProvErr := counterValue(t, metrics.ProviderErrors.WithLabelValues("mock-stream", "circuit_open"))

	_, err := gw.RouteStream(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected circuit-open stream startup error")
	}

	afterReq := counterValue(t, metrics.RequestsTotal.WithLabelValues("mock-stream", "gpt-4o", "error"))
	afterProvErr := counterValue(t, metrics.ProviderErrors.WithLabelValues("mock-stream", "circuit_open"))
	if afterReq-beforeReq != 1 {
		t.Fatalf("gateway_requests_total error delta = %v, want 1", afterReq-beforeReq)
	}
	if afterProvErr-beforeProvErr != 1 {
		t.Fatalf("gateway_provider_errors_total circuit_open delta = %v, want 1", afterProvErr-beforeProvErr)
	}
}

func streamTestRequest() providers.Request {
	return providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	}
}

func drainMeteredStream(t *testing.T, ch <-chan providers.StreamChunk) {
	t.Helper()
	for range ch { //nolint:revive
	}
}

func TestGateway_RouteStream_StartupFailureTripsCircuitWithoutRoute(t *testing.T) {
	gw, err := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets: []Target{{
			VirtualKey: "flaky-stream",
			CircuitBreaker: &CircuitBreakerConfig{
				FailureThreshold: 2,
			},
		}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{
			name:   "flaky-stream",
			models: []string{"gpt-4o"},
		},
		streamErr: errors.New("stream startup failed"),
	})

	req := streamTestRequest()
	for i := 0; i < 2; i++ {
		_, err := gw.RouteStream(context.Background(), req)
		if err == nil {
			t.Fatalf("attempt %d: expected startup error", i+1)
		}
	}

	_, err = gw.RouteStream(context.Background(), req)
	if !errors.Is(err, circuitbreaker.ErrCircuitOpen) {
		t.Fatalf("expected circuit open, got %v", err)
	}
}

func TestGateway_RouteStream_FallbackSkipsNonStreamingTargetWithCircuitBreaker(t *testing.T) {
	var selected atomic.Value
	gw, err := New(Config{
		Strategy: StrategyConfig{Mode: ModeFallback},
		Targets: []Target{
			{
				VirtualKey: "plain",
				CircuitBreaker: &CircuitBreakerConfig{
					FailureThreshold: 2,
				},
			},
			{VirtualKey: "stream"},
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.RegisterProvider(&mockProvider{
		name:   "plain",
		models: []string{"gpt-4o"},
	})
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{
			name:   "stream",
			models: []string{"gpt-4o"},
		},
		streamFn: func(_ context.Context, _ providers.Request) (<-chan providers.StreamChunk, error) {
			selected.Store("stream")
			ch := make(chan providers.StreamChunk, 1)
			ch <- providers.StreamChunk{
				ID: "stream-ok",
				Choices: []providers.StreamChoice{{
					Delta: providers.MessageDelta{Content: "ok"},
				}},
			}
			close(ch)
			return ch, nil
		},
	})

	ch, err := gw.RouteStream(context.Background(), streamTestRequest())
	if err != nil {
		t.Fatalf("RouteStream error = %v, want streaming fallback target", err)
	}
	drainMeteredStream(t, ch)
	if got := selected.Load(); got != "stream" {
		t.Fatalf("selected provider = %v, want stream", got)
	}
}

func TestGateway_RouteStream_StartupCancellationDoesNotTripCircuit(t *testing.T) {
	gw, err := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets: []Target{{
			VirtualKey: "flaky-stream",
			CircuitBreaker: &CircuitBreakerConfig{
				FailureThreshold: 2,
			},
		}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{
			name:   "flaky-stream",
			models: []string{"gpt-4o"},
		},
		streamErr: context.Canceled,
	})

	req := streamTestRequest()
	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := gw.RouteStream(ctx, req)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("attempt %d: error = %v, want context.Canceled", i+1, err)
		}
	}

	gw.mu.RLock()
	cb := gw.circuitBreakers["flaky-stream"]
	gw.mu.RUnlock()
	if cb == nil {
		t.Fatal("expected circuit breaker for flaky-stream")
	}
	if cb.State() != circuitbreaker.StateClosed {
		t.Fatalf("circuit state = %v, want closed after startup cancellations", cb.State())
	}
}

func TestGateway_Route_ProviderTimeoutTripsCircuit(t *testing.T) {
	gw, err := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets: []Target{{
			VirtualKey: "slow",
			CircuitBreaker: &CircuitBreakerConfig{
				FailureThreshold: 2,
			},
		}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.RegisterProvider(&mockProvider{
		name:   "slow",
		models: []string{"gpt-4o"},
		err:    context.DeadlineExceeded,
	})

	req := providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	}
	for i := 0; i < 2; i++ {
		_, routeErr := gw.Route(context.Background(), req)
		if !errors.Is(routeErr, context.DeadlineExceeded) {
			t.Fatalf("attempt %d: error = %v, want context.DeadlineExceeded", i+1, routeErr)
		}
	}

	_, routeErr := gw.Route(context.Background(), req)
	if !errors.Is(routeErr, circuitbreaker.ErrCircuitOpen) {
		t.Fatalf("expected circuit open after provider timeouts, got %v", routeErr)
	}
}

func TestGateway_RouteStream_ProviderTimeoutTripsCircuit(t *testing.T) {
	gw, err := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets: []Target{{
			VirtualKey: "flaky-stream",
			CircuitBreaker: &CircuitBreakerConfig{
				FailureThreshold: 2,
			},
		}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{
			name:   "flaky-stream",
			models: []string{"gpt-4o"},
		},
		streamErr: context.DeadlineExceeded,
	})

	req := streamTestRequest()
	for i := 0; i < 2; i++ {
		_, err := gw.RouteStream(context.Background(), req)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("attempt %d: error = %v, want context.DeadlineExceeded", i+1, err)
		}
	}

	_, err = gw.RouteStream(context.Background(), req)
	if !errors.Is(err, circuitbreaker.ErrCircuitOpen) {
		t.Fatalf("expected circuit open after provider timeouts, got %v", err)
	}
}

func TestShouldRecordCircuitBreakerFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ctx  context.Context
		err  error
		want bool
	}{
		{
			name: "nil error",
			ctx:  context.Background(),
			err:  nil,
			want: false,
		},
		{
			name: "provider timeout with live request context",
			ctx:  context.Background(),
			err:  context.DeadlineExceeded,
			want: true,
		},
		{
			name: "client deadline with canceled request context",
			ctx: func() context.Context {
				ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				cancel()
				return ctx
			}(),
			err:  context.DeadlineExceeded,
			want: false,
		},
		{
			name: "client cancel with canceled request context",
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			}(),
			err:  context.Canceled,
			want: false,
		},
		{
			name: "provider error with live request context",
			ctx:  context.Background(),
			err:  errors.New("upstream unavailable"),
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := shouldRecordCircuitBreakerFailure(tt.ctx, tt.err); got != tt.want {
				t.Fatalf("shouldRecordCircuitBreakerFailure() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGateway_CircuitBreaker_ReleasesHalfOpenProbeForIgnoredRateLimit(t *testing.T) {
	var calls atomic.Int32
	gw, err := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets: []Target{
			{
				VirtualKey: mockProviderName,
				CircuitBreaker: &CircuitBreakerConfig{
					FailureThreshold: 1,
					SuccessThreshold: 1,
					MaxHalfThreshold: 1,
					Timeout:          "1ms",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	gw.RegisterProvider(&mockProvider{
		name:   mockProviderName,
		models: []string{"gpt-4o"},
		completeFn: func(context.Context, providers.Request) (*providers.Response, error) {
			switch calls.Add(1) {
			case 1:
				return nil, errors.New("provider API error (500): unavailable")
			case 2:
				return nil, errors.New("provider API error (429): rate limited")
			default:
				return &providers.Response{ID: "recovered", Model: "gpt-4o"}, nil
			}
		},
	})

	req := providers.Request{Model: "gpt-4o", Messages: []providers.Message{{Role: "user", Content: "hi"}}}
	if _, err := gw.Route(context.Background(), req); err == nil {
		t.Fatal("expected first provider failure to open the circuit")
	}
	time.Sleep(5 * time.Millisecond)
	if _, err := gw.Route(context.Background(), req); err == nil || !isRateLimitError(err) {
		t.Fatalf("expected ignored 429 from half-open probe, got %v", err)
	}
	resp, err := gw.Route(context.Background(), req)
	if err != nil {
		t.Fatalf("expected released half-open slot to allow recovery probe, got %v", err)
	}
	if resp.ID != "recovered" {
		t.Fatalf("response ID = %q, want recovered", resp.ID)
	}
}

func TestRecordStreamCircuitBreakerOutcome_ReleasesHalfOpenProbeForClientCancel(t *testing.T) {
	cb := circuitbreaker.New(1, 1, 1, 1*time.Millisecond)
	cb.RecordFailure()
	time.Sleep(5 * time.Millisecond)
	_ = cb.State()
	if !cb.Allow() {
		t.Fatal("expected first half-open stream probe allowed")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	recordStreamCircuitBreakerOutcome(ctx, cb, mockProviderName, context.Canceled)

	if cb.State() != circuitbreaker.StateHalfOpen {
		t.Fatalf("expected ignored client cancel to keep half-open state, got %s", cb.State())
	}
	if !cb.Allow() {
		t.Fatal("expected ignored stream outcome to release half-open probe slot")
	}
}

func TestGateway_RouteStream_FallbackSkipsOpenCircuitBreakerTarget(t *testing.T) {
	var selected atomic.Value
	gw, err := New(Config{
		Strategy: StrategyConfig{Mode: ModeFallback},
		Targets: []Target{
			{
				VirtualKey: "flaky-stream",
				CircuitBreaker: &CircuitBreakerConfig{
					FailureThreshold: 2,
				},
			},
			{VirtualKey: "healthy-stream"},
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{
			name:   "flaky-stream",
			models: []string{"gpt-4o"},
		},
		streamErr: errors.New("stream startup failed"),
	})
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{
			name:   "healthy-stream",
			models: []string{"gpt-4o"},
		},
		streamFn: func(_ context.Context, _ providers.Request) (<-chan providers.StreamChunk, error) {
			selected.Store("healthy-stream")
			ch := make(chan providers.StreamChunk, 1)
			ch <- providers.StreamChunk{
				ID: "healthy-ok",
				Choices: []providers.StreamChoice{{
					Delta: providers.MessageDelta{Content: "ok"},
				}},
			}
			close(ch)
			return ch, nil
		},
	})

	req := streamTestRequest()
	for i := 0; i < 2; i++ {
		_, err := gw.RouteStream(context.Background(), req)
		if err == nil {
			t.Fatalf("attempt %d: expected startup error to trip breaker", i+1)
		}
	}

	ch, err := gw.RouteStream(context.Background(), req)
	if err != nil {
		t.Fatalf("RouteStream error = %v, want healthy-stream fallback", err)
	}
	drainMeteredStream(t, ch)
	if got := selected.Load(); got != "healthy-stream" {
		t.Fatalf("selected provider = %v, want healthy-stream", got)
	}
}

type slowCompleteProvider struct {
	mockProvider
}

func (p *slowCompleteProvider) Complete(ctx context.Context, _ providers.Request) (*providers.Response, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(500 * time.Millisecond):
		return &providers.Response{ID: "ok"}, nil
	}
}

func TestGateway_Route_ClientDeadlineDoesNotTripCircuit(t *testing.T) {
	gw, err := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets: []Target{{
			VirtualKey: "slow",
			CircuitBreaker: &CircuitBreakerConfig{
				FailureThreshold: 2,
			},
		}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.RegisterProvider(&slowCompleteProvider{
		mockProvider: mockProvider{
			name:   "slow",
			models: []string{"gpt-4o"},
		},
	})

	req := providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	}
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		_, routeErr := gw.Route(ctx, req)
		cancel()
		if !errors.Is(routeErr, context.DeadlineExceeded) {
			t.Fatalf("attempt %d: error = %v, want context.DeadlineExceeded", i+1, routeErr)
		}
	}

	gw.mu.RLock()
	cb := gw.circuitBreakers["slow"]
	gw.mu.RUnlock()
	if cb == nil {
		t.Fatal("expected circuit breaker for slow")
	}
	if cb.State() != circuitbreaker.StateClosed {
		t.Fatalf("circuit state = %v, want closed after client deadlines", cb.State())
	}
}

func TestGateway_RouteStream_ClientDeadlineDoesNotTripCircuit(t *testing.T) {
	gw, err := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets: []Target{{
			VirtualKey: "slow-stream",
			CircuitBreaker: &CircuitBreakerConfig{
				FailureThreshold: 2,
			},
		}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{
			name:   "slow-stream",
			models: []string{"gpt-4o"},
		},
		streamFn: func(ctx context.Context, _ providers.Request) (<-chan providers.StreamChunk, error) {
			ch := make(chan providers.StreamChunk)
			go func() {
				defer close(ch)
				ticker := time.NewTicker(20 * time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						select {
						case ch <- providers.StreamChunk{
							Choices: []providers.StreamChoice{{
								Delta: providers.MessageDelta{Content: "x"},
							}},
						}:
						case <-ctx.Done():
							return
						}
					}
				}
			}()
			return ch, nil
		},
	})

	req := streamTestRequest()
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		ch, streamErr := gw.RouteStream(ctx, req)
		if streamErr != nil {
			cancel()
			t.Fatalf("attempt %d: RouteStream error = %v", i+1, streamErr)
		}
		for range ch { //nolint:revive
		}
		cancel()
	}

	gw.mu.RLock()
	cb := gw.circuitBreakers["slow-stream"]
	gw.mu.RUnlock()
	if cb == nil {
		t.Fatal("expected circuit breaker for slow-stream")
	}
	if cb.State() != circuitbreaker.StateClosed {
		t.Fatalf("circuit state = %v, want closed after client deadlines", cb.State())
	}
}

func TestGateway_RouteStream_MidStreamFailureTripsCircuit(t *testing.T) {
	streamErr := errors.New("mid-stream provider failure")
	gw, err := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets: []Target{{
			VirtualKey: "flaky-stream",
			CircuitBreaker: &CircuitBreakerConfig{
				FailureThreshold: 2,
			},
		}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{
			name:   "flaky-stream",
			models: []string{"gpt-4o"},
		},
		streamFn: func(_ context.Context, _ providers.Request) (<-chan providers.StreamChunk, error) {
			ch := make(chan providers.StreamChunk, 2)
			ch <- providers.StreamChunk{
				Choices: []providers.StreamChoice{{
					Delta: providers.MessageDelta{Content: "partial"},
				}},
			}
			ch <- providers.StreamChunk{Error: streamErr}
			close(ch)
			return ch, nil
		},
	})

	req := streamTestRequest()
	for i := 0; i < 2; i++ {
		ch, err := gw.RouteStream(context.Background(), req)
		if err != nil {
			t.Fatalf("attempt %d: RouteStream error = %v", i+1, err)
		}
		drainMeteredStream(t, ch)
	}

	_, err = gw.RouteStream(context.Background(), req)
	if !errors.Is(err, circuitbreaker.ErrCircuitOpen) {
		t.Fatalf("expected circuit open after mid-stream failures, got %v", err)
	}
}

func TestGateway_RouteStream_BeforePluginCanSetNilRequest(t *testing.T) {
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "missing"}},
	})

	_ = gw.RegisterPlugin(plugin.StageBeforeRequest, &testPlugin{
		name: "nil-request",
		typ:  plugin.TypeGuardrail,
		execFn: func(_ context.Context, pctx *plugin.Context) error {
			pctx.Request = nil
			return nil
		},
	})

	_, err := gw.RouteStream(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error for missing streaming provider")
	}
}

func TestGateway_RouteStream_RunAfterReceivesStreamResponse(t *testing.T) {
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: mockProviderName}},
	})
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: mockProviderName, models: []string{"gpt-4o"}},
		streamFn: func(context.Context, providers.Request) (<-chan providers.StreamChunk, error) {
			ch := make(chan providers.StreamChunk, 3)
			ch <- providers.StreamChunk{
				ID:      "chatcmpl-stream",
				Object:  "chat.completion.chunk",
				Created: 123,
				Model:   "gpt-4o",
				Choices: []providers.StreamChoice{{
					Index: 0,
					Delta: providers.MessageDelta{Role: "assistant", Content: "hello "},
				}},
			}
			ch <- providers.StreamChunk{
				ID:      "chatcmpl-stream",
				Object:  "chat.completion.chunk",
				Created: 123,
				Model:   "gpt-4o",
				Choices: []providers.StreamChoice{{
					Index:        0,
					Delta:        providers.MessageDelta{Content: "world"},
					FinishReason: "stop",
				}},
			}
			ch <- providers.StreamChunk{
				ID:      "chatcmpl-stream",
				Object:  "chat.completion.chunk",
				Created: 123,
				Model:   "gpt-4o",
				Usage:   &providers.Usage{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5},
			}
			close(ch)
			return ch, nil
		},
	})

	var afterCalls int
	_ = gw.RegisterPlugin(plugin.StageAfterRequest, &testPlugin{
		name: "after",
		typ:  plugin.TypeLogging,
		execFn: func(_ context.Context, pctx *plugin.Context) error {
			afterCalls++
			if pctx.Request == nil {
				t.Fatal("after plugin request is nil")
			}
			if pctx.Response == nil {
				t.Fatal("after plugin response is nil")
			}
			if pctx.Response.Provider != mockProviderName {
				t.Fatalf("after plugin provider = %q, want mock", pctx.Response.Provider)
			}
			if pctx.Response.Model != "gpt-4o" {
				t.Fatalf("after plugin model = %q, want gpt-4o", pctx.Response.Model)
			}
			if pctx.Response.Usage.TotalTokens != 5 {
				t.Fatalf("after plugin total tokens = %d, want 5", pctx.Response.Usage.TotalTokens)
			}
			if got := pctx.Response.Choices[0].Message.Content; got != "hello world" {
				t.Fatalf("after plugin content = %q, want hello world", got)
			}
			return nil
		},
	})

	ch, err := gw.RouteStream(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
		Stream:   true,
	})
	if err != nil {
		t.Fatalf("RouteStream: %v", err)
	}
	drainStream(t, ch)

	if afterCalls != 1 {
		t.Fatalf("after plugin calls = %d, want 1", afterCalls)
	}
}

func TestGateway_RouteStream_RunOnErrorReceivesStreamError(t *testing.T) {
	streamErr := errors.New("stream failed")
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: mockProviderName}},
	})
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: mockProviderName, models: []string{"gpt-4o"}},
		streamFn: func(context.Context, providers.Request) (<-chan providers.StreamChunk, error) {
			ch := make(chan providers.StreamChunk, 1)
			ch <- providers.StreamChunk{Error: streamErr}
			close(ch)
			return ch, nil
		},
	})

	var onErrorCalls int
	_ = gw.RegisterPlugin(plugin.StageOnError, &testPlugin{
		name: "on-error",
		typ:  plugin.TypeLogging,
		execFn: func(_ context.Context, pctx *plugin.Context) error {
			onErrorCalls++
			if !errors.Is(pctx.Error, streamErr) {
				t.Fatalf("on-error plugin error = %v, want %v", pctx.Error, streamErr)
			}
			if pctx.Response != nil {
				t.Fatalf("on-error plugin response = %#v, want nil", pctx.Response)
			}
			return nil
		},
	})

	ch, err := gw.RouteStream(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
		Stream:   true,
	})
	if err != nil {
		t.Fatalf("RouteStream: %v", err)
	}
	for chunk := range ch {
		if !errors.Is(chunk.Error, streamErr) {
			t.Fatalf("stream chunk error = %v, want %v", chunk.Error, streamErr)
		}
	}

	if onErrorCalls != 1 {
		t.Fatalf("on-error plugin calls = %d, want 1", onErrorCalls)
	}
}

func TestGateway_RouteStream_AfterPluginRejectRunsOnError(t *testing.T) {
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: mockProviderName}},
	})
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: mockProviderName, models: []string{"gpt-4o"}},
		streamFn: func(context.Context, providers.Request) (<-chan providers.StreamChunk, error) {
			ch := make(chan providers.StreamChunk, 2)
			ch <- providers.StreamChunk{
				Model: "gpt-4o",
				Choices: []providers.StreamChoice{{
					Index:        0,
					Delta:        providers.MessageDelta{Role: "assistant", Content: "ok"},
					FinishReason: "stop",
				}},
			}
			ch <- providers.StreamChunk{
				Usage: &providers.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
			}
			close(ch)
			return ch, nil
		},
	})

	var onErrorCalls int
	_ = gw.RegisterPlugin(plugin.StageAfterRequest, &testPlugin{
		name: "after",
		typ:  plugin.TypeLogging,
		execFn: func(_ context.Context, pctx *plugin.Context) error {
			pctx.Reject = true
			pctx.Reason = "after plugin rejected"
			return nil
		},
	})
	_ = gw.RegisterPlugin(plugin.StageOnError, &testPlugin{
		name: "on-error",
		typ:  plugin.TypeLogging,
		execFn: func(_ context.Context, pctx *plugin.Context) error {
			onErrorCalls++
			var rejection *plugin.RejectionError
			if !errors.As(pctx.Error, &rejection) {
				t.Fatalf("on-error plugin error = %T(%v), want *plugin.RejectionError", pctx.Error, pctx.Error)
			}
			return nil
		},
	})

	ch, err := gw.RouteStream(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
		Stream:   true,
	})
	if err != nil {
		t.Fatalf("RouteStream: %v", err)
	}
	var sawPluginErr bool
	for chunk := range ch {
		if chunk.Error != nil {
			sawPluginErr = true
		}
	}
	if !sawPluginErr {
		t.Fatal("expected stream chunk carrying after plugin rejection error")
	}
	if onErrorCalls != 1 {
		t.Fatalf("on-error plugin calls = %d, want 1", onErrorCalls)
	}
}

func TestGateway_Route_AfterPluginRejectRunsOnError(t *testing.T) {
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: mockProviderName}},
	})
	gw.RegisterProvider(&mockProvider{
		name:   mockProviderName,
		models: []string{"gpt-4o"},
		resp:   &providers.Response{ID: "r1", Model: "gpt-4o", Provider: mockProviderName},
	})

	var onErrorCalls int
	_ = gw.RegisterPlugin(plugin.StageAfterRequest, &testPlugin{
		name: "after",
		typ:  plugin.TypeLogging,
		execFn: func(_ context.Context, pctx *plugin.Context) error {
			pctx.Reject = true
			pctx.Reason = "after plugin rejected"
			return nil
		},
	})
	_ = gw.RegisterPlugin(plugin.StageOnError, &testPlugin{
		name: "on-error",
		typ:  plugin.TypeLogging,
		execFn: func(_ context.Context, pctx *plugin.Context) error {
			onErrorCalls++
			var rejection *plugin.RejectionError
			if !errors.As(pctx.Error, &rejection) {
				t.Fatalf("on-error plugin error = %T(%v), want *plugin.RejectionError", pctx.Error, pctx.Error)
			}
			return nil
		},
	})

	_, err := gw.Route(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected after plugin rejection")
	}
	if onErrorCalls != 1 {
		t.Fatalf("on-error plugin calls = %d, want 1", onErrorCalls)
	}
}

func TestGateway_RouteStream_ResponseCacheHitSkipsProvider(t *testing.T) {
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: mockProviderName}},
	})
	var streamCalls atomic.Int32
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: mockProviderName, models: []string{"gpt-4o"}},
		streamFn: func(context.Context, providers.Request) (<-chan providers.StreamChunk, error) {
			streamCalls.Add(1)
			ch := make(chan providers.StreamChunk, 2)
			ch <- providers.StreamChunk{
				ID:      "chatcmpl-cacheable",
				Object:  "chat.completion.chunk",
				Created: 123,
				Model:   "gpt-4o",
				Choices: []providers.StreamChoice{{
					Index:        0,
					Delta:        providers.MessageDelta{Role: "assistant", Content: "cached"},
					FinishReason: "stop",
				}},
			}
			ch <- providers.StreamChunk{
				ID:      "chatcmpl-cacheable",
				Object:  "chat.completion.chunk",
				Created: 123,
				Model:   "gpt-4o",
				Usage:   &providers.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
			}
			close(ch)
			return ch, nil
		},
	})
	cache := &cacheplugin.ResponseCache{}
	if err := cache.Init(map[string]any{"max_age": 60}); err != nil {
		t.Fatalf("cache init: %v", err)
	}
	_ = gw.RegisterPlugin(plugin.StageBeforeRequest, cache)
	_ = gw.RegisterPlugin(plugin.StageAfterRequest, cache)

	req := providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
		Stream:   true,
	}
	first, err := gw.RouteStream(context.Background(), req)
	if err != nil {
		t.Fatalf("first RouteStream: %v", err)
	}
	drainStream(t, first)
	if got := streamCalls.Load(); got != 1 {
		t.Fatalf("stream calls after first request = %d, want 1", got)
	}

	second, err := gw.RouteStream(context.Background(), req)
	if err != nil {
		t.Fatalf("second RouteStream: %v", err)
	}
	var content string
	var usage *providers.Usage
	for chunk := range second {
		if chunk.Error != nil {
			t.Fatalf("cached stream chunk error: %v", chunk.Error)
		}
		for _, choice := range chunk.Choices {
			content += choice.Delta.Content
		}
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
	}
	if got := streamCalls.Load(); got != 1 {
		t.Fatalf("stream calls after cache hit = %d, want 1", got)
	}
	if content != "cached" {
		t.Fatalf("cached stream content = %q, want cached", content)
	}
	if usage == nil || usage.TotalTokens != 2 {
		t.Fatalf("cached stream usage = %#v, want total tokens 2", usage)
	}
}

func TestGatewayClose_IsIdempotent(t *testing.T) {
	gw, err := New(Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// gateMockProvider blocks in Complete until release is closed, so tests can
// overlap provider execution with Gateway.Close().
type gateMockProvider struct {
	mockProvider
	release     chan struct{}
	active      atomic.Int32
	releaseOnce sync.Once
}

func newGateMockProvider(resp *providers.Response, err error) *gateMockProvider {
	return &gateMockProvider{
		mockProvider: mockProvider{
			name:   "gate",
			models: []string{"gpt-4o"},
			resp:   resp,
			err:    err,
		},
		release: make(chan struct{}),
	}
}

func (p *gateMockProvider) Complete(_ context.Context, _ providers.Request) (*providers.Response, error) {
	p.active.Add(1)
	<-p.release
	if p.err != nil {
		return nil, p.err
	}
	if p.resp == nil {
		return nil, nil
	}
	resp := *p.resp
	return &resp, nil
}

func (p *gateMockProvider) releaseAll() {
	p.releaseOnce.Do(func() { close(p.release) })
}

func (p *gateMockProvider) waitActive(t *testing.T, want int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for p.active.Load() < want {
		if time.Now().After(deadline) {
			t.Fatalf("active routes = %d, want %d", p.active.Load(), want)
		}
		time.Sleep(time.Millisecond)
	}
}

// gateStreamProvider blocks before emitting stream chunks until release closes.
type gateStreamProvider struct {
	mockStreamProvider
	enterOnce sync.Once
	enter     chan struct{}
	release   chan struct{}
}

func newGateStreamProvider() *gateStreamProvider {
	return &gateStreamProvider{
		mockStreamProvider: mockStreamProvider{
			mockProvider: mockProvider{
				name:   "gate-stream",
				models: []string{"gpt-4o"},
			},
		},
		enter:   make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (p *gateStreamProvider) CompleteStream(_ context.Context, _ providers.Request) (<-chan providers.StreamChunk, error) {
	ch := make(chan providers.StreamChunk, 1)
	go func() {
		p.enterOnce.Do(func() { close(p.enter) })
		<-p.release
		ch <- providers.StreamChunk{
			ID:     "stream-1",
			Object: "chat.completion.chunk",
			Model:  "gpt-4o",
			Choices: []providers.StreamChoice{{
				Index: 0,
				Delta: providers.MessageDelta{Role: "assistant", Content: "hi"},
			}},
		}
		close(ch)
	}()
	return ch, nil
}

func completedHookEvent(traceID string) events.HookEvent {
	return events.CompletedRequest(
		traceID,
		mockProviderName,
		"gpt-4o",
		time.Millisecond,
		false,
		1,
		1,
		models.CostResult{},
		true,
	)
}

// TestGateway_PublishEvent_DetachesCancellationButPreservesValues covers
// issue #181: async event hooks must run with a context that has shed the
// request's cancellation (they fire after the HTTP handler returns) yet still
// carry the request's trace context / values via context.WithoutCancel.
func TestGateway_PublishEvent_DetachesCancellationButPreservesValues(t *testing.T) {
	gw, err := New(Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = gw.Close() })

	type ctxKey string
	const marker ctxKey = "trace-marker"

	got := make(chan context.Context, 1)
	gw.AddHook(func(ctx context.Context, _ string, _ map[string]any) {
		got <- ctx
	})

	// The request context is already cancelled by the time the hook runs.
	reqCtx, cancel := context.WithCancel(context.WithValue(context.Background(), marker, "trace-xyz"))
	cancel()

	gw.publishEvent(reqCtx, completedHookEvent("trace-1"))

	select {
	case hookCtx := <-got:
		if err := hookCtx.Err(); err != nil {
			t.Fatalf("hook ctx should be detached from cancellation, got %v", err)
		}
		if v, _ := hookCtx.Value(marker).(string); v != "trace-xyz" {
			t.Fatalf("hook ctx lost request trace value: got %q, want trace-xyz", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("hook was not dispatched")
	}
}

func runWithPanicCapture(t *testing.T, fn func()) any {
	t.Helper()
	done := make(chan any, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- r
				return
			}
			done <- nil
		}()
		fn()
	}()

	select {
	case r := <-done:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for goroutine")
		return nil
	}
}

func newHookedGateway(t *testing.T, provider providers.Provider) (*Gateway, *gateMockProvider) {
	t.Helper()
	gw, err := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: provider.Name()}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gate, ok := provider.(*gateMockProvider)
	if !ok {
		t.Fatalf("provider must be *gateMockProvider, got %T", provider)
	}
	gw.RegisterProvider(gate)
	gw.AddHook(func(context.Context, string, map[string]any) {})
	return gw, gate
}

func TestGateway_Close_DuringInFlightRouteDoesNotPanic(t *testing.T) {
	provider := newGateMockProvider(&providers.Response{ID: "ok", Model: "gpt-4o"}, nil)
	gw, gate := newHookedGateway(t, provider)

	routeDone := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				routeDone <- fmt.Errorf("panic: %v", r)
			}
		}()
		_, err := gw.Route(context.Background(), providers.Request{
			Model:    "gpt-4o",
			Messages: []providers.Message{{Role: "user", Content: "hi"}},
		})
		routeDone <- err
	}()

	gate.waitActive(t, 1)
	if err := gw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	gate.releaseAll()

	select {
	case err := <-routeDone:
		if err != nil {
			t.Fatalf("Route failed during shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for in-flight route")
	}
}

func TestGateway_Close_DuringInFlightRouteStreamDoesNotPanic(t *testing.T) {
	gw, err := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "gate-stream"}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	streamProvider := newGateStreamProvider()
	gw.RegisterProvider(streamProvider)
	gw.AddHook(func(context.Context, string, map[string]any) {})

	routeDone := make(chan any, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				routeDone <- r
				return
			}
			routeDone <- nil
		}()
		ch, err := gw.RouteStream(context.Background(), providers.Request{
			Model:    "gpt-4o",
			Messages: []providers.Message{{Role: "user", Content: "hi"}},
			Stream:   true,
		})
		if err != nil {
			routeDone <- err
			return
		}
		// Drain the stream.
		//nolint:revive // intentionally draining the stream channel to completion
		for range ch {
		}
		routeDone <- nil
	}()

	select {
	case <-streamProvider.enter:
	case <-time.After(time.Second):
		t.Fatal("stream provider never started")
	}

	if err := gw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	close(streamProvider.release)

	select {
	case result := <-routeDone:
		if result != nil {
			t.Fatalf("RouteStream failed or panicked: %v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for stream route to finish")
	}
}

func TestGateway_Close_DuringFailedRouteDoesNotPanic(t *testing.T) {
	provider := newGateMockProvider(nil, fmt.Errorf("provider down"))
	gw, gate := newHookedGateway(t, provider)

	routeDone := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				routeDone <- fmt.Errorf("panic: %v", r)
			}
		}()
		_, err := gw.Route(context.Background(), providers.Request{
			Model:    "gpt-4o",
			Messages: []providers.Message{{Role: "user", Content: "hi"}},
		})
		routeDone <- err
	}()

	gate.waitActive(t, 1)
	if err := gw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	gate.releaseAll()

	select {
	case err := <-routeDone:
		if err == nil {
			t.Fatal("expected route error, got nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for failed route")
	}
}

func TestGateway_PublishEvent_AfterCloseDoesNotPanic(t *testing.T) {
	gw, err := New(Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.AddHook(func(context.Context, string, map[string]any) {})

	if err := gw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if p := runWithPanicCapture(t, func() {
		gw.publishEvent(context.Background(), completedHookEvent("trace-after-close"))
	}); p != nil {
		t.Fatalf("publishEvent panicked after Close: %v", p)
	}
}

func TestGateway_PublishEvent_AfterShutdownWithFullQueueDoesNotPanic(t *testing.T) {
	gw := &Gateway{
		hookDispatchQ: make(chan hookDispatch, 1),
	}
	ctx, cancel := context.WithCancel(context.Background())
	gw.shutdownCtx = ctx
	gw.shutdownCancel = cancel
	gw.hookSnapshot.Store([]EventHookFunc{
		func(context.Context, string, map[string]any) {},
		func(context.Context, string, map[string]any) {},
	})
	gw.startHookWorkers()
	t.Cleanup(cancel)

	// Fill the queue so the next publishEvent hits the default branch.
	gw.publishEvent(context.Background(), completedHookEvent("trace-fill"))

	gw.shutdownCancel()

	if p := runWithPanicCapture(t, func() {
		gw.publishEvent(context.Background(), completedHookEvent("trace-shutdown-full"))
	}); p != nil {
		t.Fatalf("publishEvent panicked after shutdown with full queue: %v", p)
	}
}

func TestGateway_Close_ConcurrentPublishEventStress(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping concurrent hook shutdown stress in -short")
	}

	gw, err := New(Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.AddHook(func(context.Context, string, map[string]any) {
		time.Sleep(2 * time.Millisecond)
	})

	const publishers = 32
	panicCh := make(chan any, publishers)
	var wg sync.WaitGroup
	for range publishers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					panicCh <- r
				}
			}()
			for range 50 {
				gw.publishEvent(context.Background(), completedHookEvent("trace-stress"))
			}
		}()
	}

	time.Sleep(5 * time.Millisecond)
	if err := gw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	wg.Wait()
	close(panicCh)
	for p := range panicCh {
		t.Fatalf("concurrent publishEvent panicked during Close: %v", p)
	}
}

func TestGateway_Close_DuringConcurrentRoutesDoesNotPanic(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping concurrent route shutdown stress in -short")
	}

	gw, err := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "gate"}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	provider := newGateMockProvider(&providers.Response{ID: "ok", Model: "gpt-4o"}, nil)
	gw.RegisterProvider(provider)
	gw.AddHook(func(context.Context, string, map[string]any) {
		time.Sleep(time.Millisecond)
	})

	const routes = 16
	panicCh := make(chan any, routes)
	var wg sync.WaitGroup

	for range routes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					panicCh <- r
				}
			}()
			_, err := gw.Route(context.Background(), providers.Request{
				Model:    "gpt-4o",
				Messages: []providers.Message{{Role: "user", Content: "hi"}},
			})
			if err != nil {
				panicCh <- err
			}
		}()
	}

	provider.waitActive(t, int32(routes))

	if err := gw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	provider.releaseAll()

	wg.Wait()
	close(panicCh)
	for p := range panicCh {
		t.Fatalf("concurrent Route panicked during Close: %v", p)
	}
}

func TestGateway_Close_MultipleHooksDuringRouteDoesNotPanic(t *testing.T) {
	provider := newGateMockProvider(&providers.Response{ID: "ok", Model: "gpt-4o"}, nil)
	gw, err := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "gate"}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.RegisterProvider(provider)
	for range 3 {
		gw.AddHook(func(context.Context, string, map[string]any) {})
	}

	routeDone := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				routeDone <- fmt.Errorf("panic: %v", r)
			}
		}()
		_, err := gw.Route(context.Background(), providers.Request{
			Model:    "gpt-4o",
			Messages: []providers.Message{{Role: "user", Content: "hi"}},
		})
		routeDone <- err
	}()

	provider.waitActive(t, 1)
	if err := gw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	provider.releaseAll()

	select {
	case err := <-routeDone:
		if err != nil {
			t.Fatalf("Route failed or panicked with multiple hooks: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for route with multiple hooks")
	}
}

func TestGateway_PublishEvent_NoHooks(_ *testing.T) {
	gw := &Gateway{
		hookDispatchQ: make(chan hookDispatch, 1),
	}
	gw.hookSnapshot.Store([]EventHookFunc{})

	gw.publishEvent(context.Background(), completedHookEvent("no-hooks"))
}

func TestGateway_Route_ProviderNotFound(t *testing.T) {
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "missing"}},
	})

	_, err := gw.Route(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error for missing provider")
	}
}

func TestGateway_Route_HookPanicIsRecovered(t *testing.T) {
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: mockProviderName}},
	})
	gw.RegisterProvider(&mockProvider{
		name:   mockProviderName,
		models: []string{"gpt-4o"},
		resp:   &providers.Response{ID: "ok", Model: "gpt-4o"},
	})

	hookCalled := make(chan struct{}, 1)
	gw.AddHook(func(context.Context, string, map[string]any) {
		hookCalled <- struct{}{}
		panic("boom")
	})

	_, err := gw.Route(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected route error: %v", err)
	}

	select {
	case <-hookCalled:
	case <-time.After(time.Second):
		t.Fatal("hook was not called")
	}
}

func TestGateway_PublishEvent_CallsAllHooks(t *testing.T) {
	gw, _ := New(Config{})

	called := make(chan string, 2)
	gw.AddHook(func(context.Context, string, map[string]any) {
		called <- "first"
	})
	gw.AddHook(func(context.Context, string, map[string]any) {
		called <- "second"
	})

	gw.publishEvent(context.Background(), events.CompletedRequest(
		"trace-123",
		mockProviderName,
		"gpt-4o",
		time.Millisecond,
		false,
		1,
		1,
		models.CostResult{},
		true,
	))

	got := map[string]bool{}
	timeout := time.After(time.Second)
	for len(got) < 2 {
		select {
		case name := <-called:
			got[name] = true
		case <-timeout:
			t.Fatalf("timed out waiting for hooks, got %v", got)
		}
	}
}

func TestGateway_PublishEvent_EnqueuesEachHookIndividually(t *testing.T) {
	gw := &Gateway{
		hookDispatchQ: make(chan hookDispatch, 2),
	}

	gw.hookSnapshot.Store([]EventHookFunc{
		func(context.Context, string, map[string]any) {},
		func(context.Context, string, map[string]any) {},
	})

	gw.publishEvent(context.Background(), events.CompletedRequest(
		"trace-123",
		mockProviderName,
		"gpt-4o",
		time.Millisecond,
		false,
		1,
		1,
		models.CostResult{},
		true,
	))

	if got := len(gw.hookDispatchQ); got != 2 {
		t.Fatalf("queued hook dispatches = %d, want 2 (one per hook)", got)
	}
}

func TestRunHookDispatch_CreatesFreshPayloadMapPerHook(t *testing.T) {
	event := events.CompletedRequest(
		"trace-123",
		mockProviderName,
		"gpt-4o",
		time.Millisecond,
		false,
		1,
		1,
		models.CostResult{},
		true,
	)

	var firstData map[string]any
	runHookDispatch(hookDispatch{
		ctx:   context.Background(),
		event: event,
		hook: func(_ context.Context, _ string, data map[string]any) {
			firstData = data
			data["provider"] = "mutated"
		},
	})

	var secondProvider string
	runHookDispatch(hookDispatch{
		ctx:   context.Background(),
		event: event,
		hook: func(_ context.Context, _ string, data map[string]any) {
			secondProvider, _ = data["provider"].(string)
		},
	})

	if got := firstData["provider"]; got != "mutated" {
		t.Fatalf("first hook provider = %v, want mutated", got)
	}
	if secondProvider != mockProviderName {
		t.Fatalf("second hook provider = %q, want mock", secondProvider)
	}
}

func TestGateway_PublishEvent_IncrementsDropMetricWhenQueueFull(t *testing.T) {
	counter := metrics.HookEventsDroppedTotal.WithLabelValues(SubjectRequestCompleted)
	before := counterValue(t, counter)

	gw := &Gateway{
		hookDispatchQ: make(chan hookDispatch, 1),
	}
	gw.hookSnapshot.Store([]EventHookFunc{
		func(context.Context, string, map[string]any) {},
	})

	// Fill the queue.
	gw.publishEvent(context.Background(), events.CompletedRequest(
		"trace-fill", mockProviderName, "gpt-4o", time.Millisecond, false, 1, 1, models.CostResult{}, true,
	))
	// This one should be dropped.
	gw.publishEvent(context.Background(), events.CompletedRequest(
		"trace-drop", mockProviderName, "gpt-4o", time.Millisecond, false, 1, 1, models.CostResult{}, true,
	))

	after := counterValue(t, counter)
	if delta := after - before; delta != 1 {
		t.Fatalf("dropped hook metric delta = %v, want 1", delta)
	}
}

// testPlugin is a mock plugin for gateway tests.
type testPlugin struct {
	name    string
	typ     plugin.PluginType
	execFn  func(ctx context.Context, pctx *plugin.Context) error
	closeFn func() error
}

func (p *testPlugin) Name() string              { return p.name }
func (p *testPlugin) Type() plugin.PluginType   { return p.typ }
func (p *testPlugin) Init(map[string]any) error { return nil }
func (p *testPlugin) Execute(ctx context.Context, pctx *plugin.Context) error {
	if p.execFn != nil {
		return p.execFn(ctx, pctx)
	}
	return nil
}
func (p *testPlugin) Close() error {
	if p.closeFn != nil {
		return p.closeFn()
	}
	return nil
}

func TestGateway_Route_WithBeforePlugin(t *testing.T) {
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: mockProviderName}},
	})
	gw.RegisterProvider(&mockProvider{
		name:   mockProviderName,
		models: []string{"gpt-4o"},
		resp:   &providers.Response{ID: "ok"},
	})

	called := false
	_ = gw.RegisterPlugin(plugin.StageBeforeRequest, &testPlugin{
		name: "tracker",
		typ:  plugin.TypeGuardrail,
		execFn: func(_ context.Context, _ *plugin.Context) error {
			called = true
			return nil
		},
	})

	_, err := gw.Route(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("before-request plugin was not called")
	}
}

func TestGateway_Route_PluginRejectsRequest(t *testing.T) {
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: mockProviderName}},
	})
	gw.RegisterProvider(&mockProvider{
		name:   mockProviderName,
		models: []string{"gpt-4o"},
		resp:   &providers.Response{ID: "should-not-reach"},
	})

	_ = gw.RegisterPlugin(plugin.StageBeforeRequest, &testPlugin{
		name: "blocker",
		typ:  plugin.TypeGuardrail,
		execFn: func(_ context.Context, pctx *plugin.Context) error {
			pctx.Reject = true
			pctx.Reason = "PII detected"
			return nil
		},
	})

	_, err := gw.Route(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected rejection error")
	}
}

func init() {
	plugin.RegisterFactory("test-plugin", func() plugin.Plugin {
		return &testPlugin{name: "test-plugin", typ: plugin.TypeGuardrail}
	})
}

func TestGateway_LoadPlugins(t *testing.T) {
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: mockProviderName}},
		Plugins: []PluginConfig{
			{
				Name:    "test-plugin",
				Type:    "guardrail",
				Stage:   "before_request",
				Enabled: true,
				Config:  map[string]any{},
			},
		},
	})
	gw.RegisterProvider(&mockProvider{
		name:   mockProviderName,
		models: []string{"gpt-4o"},
		resp:   &providers.Response{ID: "ok"},
	})

	if err := gw.LoadPlugins(); err != nil {
		t.Fatalf("LoadPlugins failed: %v", err)
	}
	if !gw.plugins.HasPlugins() {
		t.Error("expected plugins to be registered")
	}
}

func TestGateway_LoadPlugins_UnknownPlugin(t *testing.T) {
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: mockProviderName}},
		Plugins: []PluginConfig{
			{
				Name:    "does-not-exist",
				Type:    "guardrail",
				Stage:   "before_request",
				Enabled: true,
				Config:  map[string]any{},
			},
		},
	})

	err := gw.LoadPlugins()
	if err == nil {
		t.Fatal("expected error for unknown plugin")
	}
	if got := err.Error(); got != "unknown plugin: does-not-exist" {
		t.Errorf("got error %q, want %q", got, "unknown plugin: does-not-exist")
	}
}

func TestGateway_ReloadConfig_ClosesOldPlugins(t *testing.T) {
	var oldClosed atomic.Int32
	plugin.RegisterFactory("test-close-plugin", func() plugin.Plugin {
		return &testPlugin{
			name: "test-close-plugin",
			typ:  plugin.TypeLogging,
			closeFn: func() error {
				oldClosed.Add(1)
				return nil
			},
		}
	})

	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: mockProviderName}},
		Plugins: []PluginConfig{
			{
				Name:    "test-close-plugin",
				Type:    "logging",
				Stage:   "after_request",
				Enabled: true,
				Config:  map[string]any{},
			},
		},
	})
	if err := gw.LoadPlugins(); err != nil {
		t.Fatalf("LoadPlugins failed: %v", err)
	}

	if err := gw.ReloadConfig(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: mockProviderName}},
	}); err != nil {
		t.Fatalf("ReloadConfig failed: %v", err)
	}
	if got := oldClosed.Load(); got != 1 {
		t.Fatalf("old plugin closes = %d, want 1", got)
	}
	if gw.plugins.HasPlugins() {
		t.Fatal("expected reload without plugin configs to clear registered plugins")
	}
}

func TestGateway_ReloadConfig_DefersOldPluginCloseUntilInFlightRouteFinishes(t *testing.T) {
	provider := newGateMockProvider(&providers.Response{ID: "ok", Model: "gpt-4o"}, nil)
	gw, err := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: provider.Name()}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = gw.Close() })
	gw.RegisterProvider(provider)

	oldClosed := make(chan struct{})
	var closeOnce sync.Once
	afterRan := make(chan struct{})
	var afterOnce sync.Once
	if err := gw.RegisterPlugin(plugin.StageAfterRequest, &testPlugin{
		name: "in-flight-after",
		typ:  plugin.TypeLogging,
		execFn: func(context.Context, *plugin.Context) error {
			afterOnce.Do(func() { close(afterRan) })
			return nil
		},
		closeFn: func() error {
			closeOnce.Do(func() { close(oldClosed) })
			return nil
		},
	}); err != nil {
		t.Fatalf("RegisterPlugin: %v", err)
	}

	routeDone := make(chan error, 1)
	go func() {
		_, routeErr := gw.Route(context.Background(), providers.Request{
			Model:    "gpt-4o",
			Messages: []providers.Message{{Role: "user", Content: "hi"}},
		})
		routeDone <- routeErr
	}()

	provider.waitActive(t, 1)
	reloadDone := make(chan error, 1)
	go func() {
		reloadDone <- gw.ReloadConfig(Config{
			Strategy: StrategyConfig{Mode: ModeSingle},
			Targets:  []Target{{VirtualKey: provider.Name()}},
		})
	}()

	select {
	case err := <-reloadDone:
		if err != nil {
			t.Fatalf("ReloadConfig: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reload blocked behind in-flight route")
	}
	select {
	case <-oldClosed:
		t.Fatal("old plugin manager closed while an in-flight route could still run after/error plugins")
	default:
	}

	provider.releaseAll()

	select {
	case err := <-routeDone:
		if err != nil {
			t.Fatalf("Route: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for route")
	}
	select {
	case <-afterRan:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for after plugin")
	}
	select {
	case <-oldClosed:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for old plugin close")
	}
}

// ── mockEmbeddingProvider ─────────────────────────────────────────────────────

type mockEmbeddingProvider struct {
	mockProvider
	capturedModel string
}

func (m *mockEmbeddingProvider) Embed(_ context.Context, req providers.EmbeddingRequest) (*providers.EmbeddingResponse, error) {
	m.capturedModel = req.Model
	return &providers.EmbeddingResponse{Model: req.Model}, nil
}

// ── mockImageProvider ─────────────────────────────────────────────────────────

type mockImageProvider struct {
	mockProvider
	capturedModel string
}

func (m *mockImageProvider) GenerateImage(_ context.Context, req providers.ImageRequest) (*providers.ImageResponse, error) {
	m.capturedModel = req.Model
	return &providers.ImageResponse{}, nil
}

// ── alias resolution tests ────────────────────────────────────────────────────

func TestGateway_Embed_ResolvesAlias(t *testing.T) {
	ep := &mockEmbeddingProvider{
		mockProvider: mockProvider{
			name:   mockProviderName,
			models: []string{"text-embedding-3-small"},
		},
	}
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: mockProviderName}},
		Aliases:  map[string]string{"my-embed": "text-embedding-3-small"},
	})
	gw.RegisterProvider(ep)

	_, err := gw.Embed(context.Background(), providers.EmbeddingRequest{
		Model: "my-embed",
		Input: "hello",
	})
	if err != nil {
		t.Fatalf("Embed() error: %v", err)
	}
	if ep.capturedModel != "text-embedding-3-small" {
		t.Errorf("provider received model %q, want text-embedding-3-small (alias not resolved)", ep.capturedModel)
	}
}

func TestGateway_Embed_NoAliasPassthrough(t *testing.T) {
	ep := &mockEmbeddingProvider{
		mockProvider: mockProvider{
			name:   mockProviderName,
			models: []string{"text-embedding-3-small"},
		},
	}
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: mockProviderName}},
	})
	gw.RegisterProvider(ep)

	_, err := gw.Embed(context.Background(), providers.EmbeddingRequest{
		Model: "text-embedding-3-small",
		Input: "hello",
	})
	if err != nil {
		t.Fatalf("Embed() error: %v", err)
	}
	if ep.capturedModel != "text-embedding-3-small" {
		t.Errorf("provider received model %q, want text-embedding-3-small", ep.capturedModel)
	}
}

func TestGateway_GenerateImage_ResolvesAlias(t *testing.T) {
	ip := &mockImageProvider{
		mockProvider: mockProvider{
			name:   mockProviderName,
			models: []string{"dall-e-3"},
		},
	}
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: mockProviderName}},
		Aliases:  map[string]string{"my-image-model": "dall-e-3"},
	})
	gw.RegisterProvider(ip)

	_, err := gw.GenerateImage(context.Background(), providers.ImageRequest{
		Model:  "my-image-model",
		Prompt: "a cat",
	})
	if err != nil {
		t.Fatalf("GenerateImage() error: %v", err)
	}
	if ip.capturedModel != "dall-e-3" {
		t.Errorf("provider received model %q, want dall-e-3 (alias not resolved)", ip.capturedModel)
	}
}

// ── StartDiscovery interval validation tests ──────────────────────────────────

func TestGateway_StartDiscovery_ZeroInterval(t *testing.T) {
	gw, _ := New(Config{})
	err := gw.StartDiscovery(context.Background(), 0)
	if err == nil {
		t.Fatal("StartDiscovery(0) should return an error")
	}
}

func TestGateway_StartDiscovery_NegativeInterval(t *testing.T) {
	gw, _ := New(Config{})
	err := gw.StartDiscovery(context.Background(), -time.Second)
	if err == nil {
		t.Fatal("StartDiscovery(-1s) should return an error")
	}
}

func TestGateway_StartDiscovery_ValidInterval(t *testing.T) {
	gw, _ := New(Config{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := gw.StartDiscovery(ctx, time.Hour)
	if err != nil {
		t.Fatalf("StartDiscovery(1h) returned unexpected error: %v", err)
	}
	// Cancel immediately; just verifies no panic and clean return.
	cancel()
}

// ─── MCP integration test ──────────────────────────────────────────────────

// multiCallProvider is a test provider that returns pre-configured responses
// in sequence, recording every request it receives for later inspection.
type multiCallProvider struct {
	name      string
	models    []string
	responses []*providers.Response
	mu        sync.Mutex
	requests  []providers.Request
}

func (m *multiCallProvider) Name() string                  { return m.name }
func (m *multiCallProvider) SupportedModels() []string     { return m.models }
func (m *multiCallProvider) Models() []providers.ModelInfo { return nil }
func (m *multiCallProvider) SupportsModel(model string) bool {
	for _, mm := range m.models {
		if mm == model {
			return true
		}
	}
	return false
}
func (m *multiCallProvider) Complete(_ context.Context, req providers.Request) (*providers.Response, error) {
	m.mu.Lock()
	idx := len(m.requests)
	m.requests = append(m.requests, req)
	m.mu.Unlock()
	if idx >= len(m.responses) {
		return nil, fmt.Errorf("multiCallProvider: no response configured for call %d", idx+1)
	}
	return m.responses[idx], nil
}
func (m *multiCallProvider) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.requests)
}

// newMCPTestServer returns a minimal httptest MCP server that exposes a
// single "get_answer" tool returning {"type":"text","text":"42"}.
func newMCPTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		defer r.Body.Close() //nolint:errcheck

		var rpcReq struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      any             `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&rpcReq); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		type rpcResp struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      any             `json:"id"`
			Result  json.RawMessage `json:"result"`
		}
		write := func(result any) {
			b, _ := json.Marshal(result)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(rpcResp{JSONRPC: "2.0", ID: rpcReq.ID, Result: b})
		}

		switch rpcReq.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "test-session-001")
			write(map[string]any{
				"name":         "test-mcp",
				"version":      "1.0",
				"capabilities": map[string]any{"tools": map[string]any{}},
			})
		case "tools/list":
			write(map[string]any{
				"tools": []map[string]any{{
					"name":        "get_answer",
					"description": "Returns the ultimate answer.",
					"inputSchema": json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`),
				}},
			})
		case "tools/call":
			write(map[string]any{
				"content": []map[string]any{{"type": "text", "text": "42"}},
				"isError": false,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

// TestGateway_Route_MCPToolInjectionAndLoop verifies the full MCP agentic loop:
//  1. MCP tools are injected into the first LLM request.
//  2. When the LLM returns tool_calls the gateway calls the MCP server and
//     appends a tool-result message before re-routing.
//  3. The loop terminates when the LLM returns a normal response.
func TestGateway_Route_MCPToolInjectionAndLoop(t *testing.T) {
	// Start a mock MCP server.
	mcpSrv := newMCPTestServer(t)
	defer mcpSrv.Close()

	// Provider call 1 — returns a tool_call for "get_answer".
	// Provider call 2 — returns the final answer after seeing the tool result.
	mp := &multiCallProvider{
		name:   "test-provider",
		models: []string{"test-model"},
		responses: []*providers.Response{
			{
				ID:    "resp-1",
				Model: "test-model",
				Choices: []providers.Choice{{
					Message: providers.Message{
						Role: "assistant",
						ToolCalls: []providers.ToolCall{{
							ID:   "tc-1",
							Type: "function",
							Function: providers.FunctionCall{
								Name:      "get_answer",
								Arguments: `{"q":"what is the answer?"}`,
							},
						}},
					},
					FinishReason: "tool_calls",
				}},
			},
			{
				ID:    "resp-2",
				Model: "test-model",
				Choices: []providers.Choice{{
					Message: providers.Message{
						Role:    "assistant",
						Content: "The answer is 42.",
					},
					FinishReason: "stop",
				}},
			},
		},
	}

	gw, err := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "test-provider"}},
		MCPServers: []mcp.ServerConfig{{
			Name:           "test-mcp",
			URL:            mcpSrv.URL + "/mcp",
			TimeoutSeconds: 5,
		}},
	})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	gw.RegisterProvider(mp)

	// Wait for MCP init (tools/list handshake) to finish.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	select {
	case <-gw.MCPInitDone():
	case <-ctx.Done():
		t.Fatal("timed out waiting for MCP initialization")
	}

	// Route the request through the agentic loop.
	resp, err := gw.Route(ctx, providers.Request{
		Model:    "test-model",
		Messages: []providers.Message{{Role: "user", Content: "What is the answer?"}},
	})
	if err != nil {
		t.Fatalf("Route() error: %v", err)
	}

	// The final response must be from the second provider call.
	if resp.ID != "resp-2" {
		t.Errorf("final response ID = %q, want resp-2", resp.ID)
	}

	// Provider must have been called exactly twice.
	if got := mp.callCount(); got != 2 {
		t.Errorf("provider called %d times, want 2", got)
	}

	mp.mu.Lock()
	requests := mp.requests
	mp.mu.Unlock()

	// First request must contain the "get_answer" tool injected by the gateway.
	var toolFound bool
	for _, tool := range requests[0].Tools {
		if tool.Function.Name == "get_answer" {
			toolFound = true
			break
		}
	}
	if !toolFound {
		t.Error("first request: get_answer tool not injected")
	}

	// Second request must contain a tool-result message for tc-1 with content "42".
	var toolMsg *providers.Message
	for i := range requests[1].Messages {
		if requests[1].Messages[i].Role == "tool" && requests[1].Messages[i].ToolCallID == "tc-1" {
			toolMsg = &requests[1].Messages[i]
			break
		}
	}
	if toolMsg == nil {
		t.Fatal("second request: missing tool-result message with ToolCallID=tc-1")
	}
	if toolMsg.Content != "42" {
		t.Errorf("tool-result content = %q, want \"42\"", toolMsg.Content)
	}
}

// TestGateway_RouteStream_MCPRedirect verifies that when MCP servers are
// configured, RouteStream routes through Route (running the full agentic loop)
// and wraps the final non-streaming response into a single-chunk channel.
func TestGateway_RouteStream_MCPRedirect(t *testing.T) {
	mcpSrv := newMCPTestServer(t)
	defer mcpSrv.Close()

	// Provider call 1 — returns a tool_call for "get_answer".
	// Provider call 2 — returns the final text after seeing the tool result.
	mp := &multiCallProvider{
		name:   "mock-mcp-stream",
		models: []string{"gpt-4o"},
		responses: []*providers.Response{
			{
				ID:    "s1",
				Model: "gpt-4o",
				Choices: []providers.Choice{{
					Message: providers.Message{
						Role: "assistant",
						ToolCalls: []providers.ToolCall{{
							ID:   "tc-stream-1",
							Type: "function",
							Function: providers.FunctionCall{
								Name:      "get_answer",
								Arguments: `{"q":"test"}`,
							},
						}},
					},
				}},
			},
			{
				ID:    "s2",
				Model: "gpt-4o",
				Choices: []providers.Choice{{
					Message: providers.Message{
						Role:    "assistant",
						Content: "The answer is 42.",
					},
					FinishReason: "stop",
				}},
			},
		},
	}

	gw, err := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "mock-mcp-stream"}},
		MCPServers: []mcp.ServerConfig{{
			Name:           "test-mcp-stream",
			URL:            mcpSrv.URL,
			TimeoutSeconds: 10,
		}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.RegisterProvider(mp)

	// Wait for MCP init to complete.
	select {
	case <-gw.MCPInitDone():
	case <-time.After(5 * time.Second):
		t.Fatal("MCP init timeout")
	}

	// RouteStream with stream=true — should redirect through Route.
	ch, err := gw.RouteStream(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Stream:   true,
		Messages: []providers.Message{{Role: "user", Content: "What is the answer?"}},
	})
	if err != nil {
		t.Fatalf("RouteStream error: %v", err)
	}

	// Drain the channel.
	var chunks []providers.StreamChunk
	for chunk := range ch {
		if chunk.Error != nil {
			t.Fatalf("stream chunk error: %v", chunk.Error)
		}
		chunks = append(chunks, chunk)
	}

	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk (MCP redirect), got %d", len(chunks))
	}
	if len(chunks[0].Choices) == 0 {
		t.Fatal("chunk has no choices")
	}
	if chunks[0].Choices[0].Delta.Content != "The answer is 42." {
		t.Errorf("chunk content = %q, want %q", chunks[0].Choices[0].Delta.Content, "The answer is 42.")
	}

	// Both provider calls must have fired (tool injection + final answer).
	if mp.callCount() != 2 {
		t.Errorf("expected 2 provider calls (agentic loop), got %d", mp.callCount())
	}
}

// mockBenchStreamProvider is a streaming provider that immediately returns a
// closed, empty channel — used only by benchmarks to avoid blocking drains.
type mockBenchStreamProvider struct {
	mockProvider
}

func (m *mockBenchStreamProvider) CompleteStream(_ context.Context, _ providers.Request) (<-chan providers.StreamChunk, error) {
	ch := make(chan providers.StreamChunk)
	close(ch)
	return ch, nil
}

// ── Benchmarks ───────────────────────────────────────────────────────────────

// silenceLogs redirects the gateway's package logger to io.Discard for the
// duration of a benchmark, preventing JSON log lines from corrupting the
// benchmark output (the gateway logger writes to os.Stdout by design).
func silenceLogs(b *testing.B) {
	b.Helper()
	prev := logging.Logger
	logging.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	b.Cleanup(func() { logging.Logger = prev })
}

// BenchmarkRoute measures the overhead of a single Route() call through a
// single-provider configuration with no plugins.
func BenchmarkRoute(b *testing.B) {
	silenceLogs(b)
	gw, err := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "bench"}},
	})
	if err != nil {
		b.Fatal(err)
	}
	gw.RegisterProvider(&mockProvider{
		name:   "bench",
		models: []string{"gpt-4o"},
		resp: &providers.Response{
			Choices: []providers.Choice{{Message: providers.Message{Role: "assistant", Content: "ok"}}},
		},
	})

	req := providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hello"}},
	}
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := gw.Route(ctx, req); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkRouteParallel measures Route() under concurrent load to exercise
// lock contention on the strategy read path.
func BenchmarkRouteParallel(b *testing.B) {
	silenceLogs(b)
	gw, err := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "bench"}},
	})
	if err != nil {
		b.Fatal(err)
	}
	gw.RegisterProvider(&mockProvider{
		name:   "bench",
		models: []string{"gpt-4o"},
		resp: &providers.Response{
			Choices: []providers.Choice{{Message: providers.Message{Role: "assistant", Content: "ok"}}},
		},
	})

	req := providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hello"}},
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		ctx := context.Background()
		for pb.Next() {
			if _, err := gw.Route(ctx, req); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkRouteStream measures the overhead of a RouteStream() call (no MCP,
// no plugins). The benchmark drains the channel to completion each iteration.
func BenchmarkRouteStream(b *testing.B) {
	silenceLogs(b)

	gw, err := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "bench-stream"}},
	})
	if err != nil {
		b.Fatal(err)
	}
	gw.RegisterProvider(&mockBenchStreamProvider{
		mockProvider: mockProvider{
			name:   "bench-stream",
			models: []string{"gpt-4o"},
		},
	})

	req := providers.Request{
		Model:    "gpt-4o",
		Stream:   true,
		Messages: []providers.Message{{Role: "user", Content: "hello"}},
	}
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out, err := gw.RouteStream(ctx, req)
		if err != nil {
			b.Fatal(err)
		}
		for range out { //nolint:revive
		}
	}
}

// BenchmarkRoute_WithPlugins measures Route() with a two-plugin before_request
// chain (word-filter + max-token) loaded via LoadPlugins.
func BenchmarkRoute_WithPlugins(b *testing.B) {
	silenceLogs(b)
	cfg := Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "bench-plugins"}},
		Plugins: []PluginConfig{
			{Name: "word-filter", Enabled: true, Stage: "before_request", Config: map[string]any{"blocked_words": []any{}}},
			{Name: "max-token", Enabled: true, Stage: "before_request", Config: map[string]any{"max_input_tokens": 1000}},
		},
	}
	gw, err := New(cfg)
	if err != nil {
		b.Fatal(err)
	}
	gw.RegisterProvider(&mockProvider{
		name:   "bench-plugins",
		models: []string{"gpt-4o"},
		resp: &providers.Response{
			Choices: []providers.Choice{{Message: providers.Message{Role: "assistant", Content: "ok"}}},
		},
	})
	if err := gw.LoadPlugins(); err != nil {
		b.Fatal(err)
	}

	req := providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hello world"}},
	}
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := gw.Route(ctx, req); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRoute_WithHook(b *testing.B) {
	silenceLogs(b)
	gw, err := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "bench-hook"}},
	})
	if err != nil {
		b.Fatal(err)
	}
	gw.RegisterProvider(&mockProvider{
		name:   "bench-hook",
		models: []string{"gpt-4o"},
		resp: &providers.Response{
			Choices: []providers.Choice{{Message: providers.Message{Role: "assistant", Content: "ok"}}},
		},
	})

	var calls atomic.Int64
	gw.AddHook(func(context.Context, string, map[string]any) {
		calls.Add(1)
	})

	req := providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hello"}},
	}
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := gw.Route(ctx, req); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()

	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() < int64(b.N) {
		if time.Now().After(deadline) {
			b.Fatalf("timed out waiting for hook dispatch: completed=%d want=%d", calls.Load(), b.N)
		}
		time.Sleep(time.Millisecond)
	}
}

// BenchmarkFindByModel measures repeated model lookup after the gateway has
// built its lookup indexes and per-model cache.
func BenchmarkFindByModel(b *testing.B) {
	silenceLogs(b)
	gw, err := New(Config{})
	if err != nil {
		b.Fatal(err)
	}
	gw.RegisterProvider(&mockProvider{
		name:   "bench-find",
		models: []string{"gpt-4o"},
		resp: &providers.Response{
			Choices: []providers.Choice{{Message: providers.Message{Role: "assistant", Content: "ok"}}},
		},
	})

	if _, ok := gw.FindByModel("gpt-4o"); !ok {
		b.Fatal("expected model lookup to succeed")
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok := gw.FindByModel("gpt-4o"); !ok {
			b.Fatal("expected model lookup to succeed")
		}
	}
}

// blockAfterFirstMock fails on its first Complete call (to trip the circuit
// breaker) and blocks on the second call until release is closed — used to
// hold a half-open probe slot while a concurrent request tests the cap.
type blockAfterFirstMock struct {
	mockProvider
	callN   atomic.Int32
	ready   chan struct{} // closed when the second call enters Complete
	release chan struct{} // closed to let the second call return
}

func (m *blockAfterFirstMock) Complete(_ context.Context, _ providers.Request) (*providers.Response, error) {
	if n := m.callN.Add(1); n == 1 {
		return nil, errors.New("provider down")
	}
	close(m.ready)
	<-m.release
	return m.resp, nil
}

// TestGateway_CircuitBreaker_MaxHalfThreshold_FromConfig verifies that the
// MaxHalfThreshold value in CircuitBreakerConfig is wired into the circuit
// breaker: while one half-open probe is in-flight, a concurrent request must
// be rejected with ErrCircuitOpen.
func TestGateway_CircuitBreaker_MaxHalfThreshold_FromConfig(t *testing.T) {
	mock := &blockAfterFirstMock{
		mockProvider: mockProvider{
			name:   "mock-cb",
			models: []string{"gpt-4o"},
			resp:   &providers.Response{ID: "ok", Model: "gpt-4o"},
		},
		ready:   make(chan struct{}),
		release: make(chan struct{}),
	}

	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets: []Target{{
			VirtualKey: "mock-cb",
			CircuitBreaker: &CircuitBreakerConfig{
				FailureThreshold: 1,
				SuccessThreshold: 1,
				MaxHalfThreshold: 1,
				Timeout:          "1ms",
			},
		}},
	})
	gw.RegisterProvider(mock)

	req := providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	}

	// Trip the circuit: first call fails → circuit opens.
	_, _ = gw.Route(context.Background(), req)

	// Wait for half-open transition.
	time.Sleep(5 * time.Millisecond)

	// Probe 1: admitted into half-open, blocks inside provider holding the slot.
	probe1Err := make(chan error, 1)
	go func() {
		_, err := gw.Route(context.Background(), req)
		probe1Err <- err
	}()

	// Wait until probe 1 is holding the in-flight slot (halfOpenProbes=1).
	select {
	case <-mock.ready:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for probe 1 to enter provider")
	}

	// Probe 2: cap=1 already consumed → must be rejected.
	_, err := gw.Route(context.Background(), req)
	if !errors.Is(err, circuitbreaker.ErrCircuitOpen) {
		t.Errorf("expected ErrCircuitOpen for second concurrent half-open probe, got %v", err)
	}

	// Release probe 1 and verify it succeeds.
	close(mock.release)
	select {
	case err := <-probe1Err:
		if err != nil {
			t.Errorf("probe 1 expected success, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for probe 1 to complete")
	}
}

func BenchmarkPublishEvent(b *testing.B) {
	silenceLogs(b)
	gw, err := New(Config{})
	if err != nil {
		b.Fatal(err)
	}

	var calls atomic.Int64
	var wg sync.WaitGroup
	wg.Add(b.N)
	gw.AddHook(func(context.Context, string, map[string]any) {
		calls.Add(1)
		wg.Done()
	})

	event := events.CompletedRequest(
		"trace-bench",
		"bench",
		"gpt-4o",
		time.Millisecond,
		false,
		1,
		1,
		models.CostResult{},
		true,
	)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		gw.publishEvent(ctx, event)
	}
	b.StopTimer()

	waitDone := make(chan struct{})
	go func() {
		defer close(waitDone)
		wg.Wait()
	}()

	select {
	case <-waitDone:
	case <-time.After(5 * time.Second):
		b.Fatalf("timed out waiting for hook dispatch: completed=%d want=%d", calls.Load(), b.N)
	}
}

// freshProvider returns a new *providers.Response on every Complete call so
// concurrent goroutines never share a response pointer. Used by race tests.
type freshProvider struct {
	name   string
	models []string
}

func (f *freshProvider) Name() string                  { return f.name }
func (f *freshProvider) SupportedModels() []string     { return f.models }
func (f *freshProvider) Models() []providers.ModelInfo { return nil }
func (f *freshProvider) SupportsModel(model string) bool {
	for _, m := range f.models {
		if m == model {
			return true
		}
	}
	return false
}
func (f *freshProvider) Complete(_ context.Context, req providers.Request) (*providers.Response, error) {
	return &providers.Response{ID: "r", Model: req.Model, Provider: f.name}, nil
}

// TestRoute_ProviderLookup_NoDataRace is the acceptance test for issue #128.
//
// The lookup closure built inside getStrategy runs inside Strategy.Execute with
// no lock held. If it reads g.providers / g.circuitBreakers directly instead of
// from a snapshot taken under lock, a concurrent RegisterProvider (or
// runDiscovery) that writes those maps will cause a fatal data race.
//
// Run with -race to verify: go test -race -run TestRoute_ProviderLookup_NoDataRace
func TestRoute_ProviderLookup_NoDataRace(t *testing.T) {
	gw, err := New(Config{
		Strategy: StrategyConfig{Mode: ModeFallback},
		Targets: []Target{
			{VirtualKey: "p1"},
			{VirtualKey: "p2"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	gw.RegisterProvider(&freshProvider{name: "p1", models: []string{"test-model"}})
	gw.RegisterProvider(&freshProvider{name: "p2", models: []string{"test-model"}})

	const routerGoroutines = 20
	const writerGoroutines = 10
	const iters = 40

	ctx := context.Background()
	req := providers.Request{
		Model:    "test-model",
		Messages: []providers.Message{{Role: roleUser, Content: "hello"}},
	}

	var wg sync.WaitGroup

	// Goroutines calling Route concurrently — these will execute the lookup
	// closure while the writers below mutate g.providers under lock.
	for i := 0; i < routerGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				_, _ = gw.Route(ctx, req)
			}
		}()
	}

	// Goroutines calling RegisterProvider concurrently — mirrors runtime
	// model discovery writing g.providers under lock (issue #128 trigger).
	for i := 0; i < writerGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iters/2; j++ {
				gw.RegisterProvider(&freshProvider{
					name:   fmt.Sprintf("dynamic-%d-%d", id, j),
					models: []string{"other-model"},
				})
			}
		}(i)
	}

	wg.Wait()
}
