package aigateway

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	cacheplugin "github.com/ferro-labs/ai-gateway/internal/plugins/cache"
	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
)

func TestGateway_RouteStream_BeforePluginCanSetNilRequest(t *testing.T) {
	gw, _ := newTestGateway(t, Config{
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
	gw, _ := newTestGateway(t, Config{
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
	gw, _ := newTestGateway(t, Config{
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
	gw, _ := newTestGateway(t, Config{
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
		typ:  plugin.TypeGuardrail,
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
	gw, _ := newTestGateway(t, Config{
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
		typ:  plugin.TypeGuardrail,
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

func TestGateway_Route_AfterLoggingPanicStaysNonFatal(t *testing.T) {
	gw, _ := newTestGateway(t, Config{
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
		name: "after-logger",
		typ:  plugin.TypeLogging,
		execFn: func(context.Context, *plugin.Context) error {
			panic("log sink down")
		},
	})
	_ = gw.RegisterPlugin(plugin.StageOnError, &testPlugin{
		name: "on-error",
		typ:  plugin.TypeLogging,
		execFn: func(context.Context, *plugin.Context) error {
			onErrorCalls++
			return nil
		},
	})

	resp, err := gw.Route(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Route() error = %v, want nil", err)
	}
	if resp.ID != "r1" {
		t.Fatalf("response ID = %q, want r1", resp.ID)
	}
	if onErrorCalls != 0 {
		t.Fatalf("on-error plugin calls = %d, want 0", onErrorCalls)
	}
}

func TestGateway_RouteStream_AfterLoggingErrorStaysNonFatal(t *testing.T) {
	gw, _ := newTestGateway(t, Config{
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
		name: "after-logger",
		typ:  plugin.TypeLogging,
		execFn: func(context.Context, *plugin.Context) error {
			return fmt.Errorf("log sink down")
		},
	})
	_ = gw.RegisterPlugin(plugin.StageOnError, &testPlugin{
		name: "on-error",
		typ:  plugin.TypeLogging,
		execFn: func(context.Context, *plugin.Context) error {
			onErrorCalls++
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
	var chunks int
	for chunk := range ch {
		chunks++
		if chunk.Error != nil {
			t.Fatalf("stream chunk error = %v, want nil", chunk.Error)
		}
	}
	if chunks == 0 {
		t.Fatal("expected stream chunks")
	}
	if onErrorCalls != 0 {
		t.Fatalf("on-error plugin calls = %d, want 0", onErrorCalls)
	}
}

func TestGateway_RouteStream_ResponseCacheHitSkipsProvider(t *testing.T) {
	gw, _ := newTestGateway(t, Config{
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
