package aigateway

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/logging"
	"github.com/ferro-labs/ai-gateway/providers"
)

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

// BenchmarkGateway_Route measures the overhead of a single Route() call through a
// single-provider configuration with no plugins.
func BenchmarkGateway_Route(b *testing.B) {
	silenceLogs(b)
	gw, err := newTestGateway(b, Config{
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

// BenchmarkGateway_RouteParallel measures Route() under concurrent load to exercise
// lock contention on the strategy read path.
func BenchmarkGateway_RouteParallel(b *testing.B) {
	silenceLogs(b)
	gw, err := newTestGateway(b, Config{
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

// BenchmarkGateway_RouteStream measures the overhead of a RouteStream() call (no MCP,
// no plugins). The benchmark drains the channel to completion each iteration.
func BenchmarkGateway_RouteStream(b *testing.B) {
	silenceLogs(b)

	gw, err := newTestGateway(b, Config{
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
		for range out { //nolint:revive // empty-block: intentionally draining the stream to completion
		}
	}
}

// BenchmarkGateway_RouteWithPlugins measures Route() with a two-plugin before_request
// chain (word-filter + max-token) loaded via LoadPlugins.
func BenchmarkGateway_RouteWithPlugins(b *testing.B) {
	silenceLogs(b)
	cfg := Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "bench-plugins"}},
		Plugins: []PluginConfig{
			{Name: "word-filter", Enabled: true, Stage: "before_request", Config: map[string]any{"blocked_words": []any{}}},
			{Name: "max-token", Enabled: true, Stage: "before_request", Config: map[string]any{"max_input_tokens": 1000}},
		},
	}
	gw, err := newTestGateway(b, cfg)
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

func BenchmarkGateway_RouteWithHook(b *testing.B) {
	silenceLogs(b)
	gw, err := newTestGateway(b, Config{
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

// BenchmarkGateway_FindByModel measures repeated model lookup after the gateway has
// built its lookup indexes and per-model cache.
func BenchmarkGateway_FindByModel(b *testing.B) {
	silenceLogs(b)
	gw, err := newTestGateway(b, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "unused"}},
	})

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
