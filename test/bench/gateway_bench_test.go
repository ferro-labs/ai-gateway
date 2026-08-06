// Gateway routing microbenchmarks.
//
// These measure the gateway's own overhead — Route, RouteStream, the plugin
// chain, the async hook bus, and model lookup — over a trivial in-memory
// provider that does no wire translation, so the numbers isolate the routing
// hot path rather than any provider or network cost.
//
//	go test -bench=Gateway -benchmem ./test/bench/     # or: make bench
package bench

import (
	"context"
	"io"
	"sync/atomic"
	"testing"
	"time"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/pkg/logger"
	"github.com/ferro-labs/ai-gateway/providers"

	// Register the built-in plugins the plugin-chain benchmark loads.
	_ "github.com/ferro-labs/ai-gateway/plugin/maxtoken"
	_ "github.com/ferro-labs/ai-gateway/plugin/wordfilter"
)

// benchProvider is a trivial in-memory provider: it returns a canned response
// with no marshalling, so a benchmark measures gateway overhead alone. It
// serves the models it declares via ConfiguredModels, which is what places it
// in the routing index.
type benchProvider struct {
	name   string
	models []string
	resp   *providers.Response
}

func (m *benchProvider) Name() string                  { return m.name }
func (m *benchProvider) ConfiguredModels() []string    { return m.models }
func (m *benchProvider) Models() []providers.ModelInfo { return nil }

func (m *benchProvider) SupportsModel(model string) bool {
	for _, mm := range m.models {
		if mm == model {
			return true
		}
	}
	return false
}

func (m *benchProvider) Complete(context.Context, providers.Request) (*providers.Response, error) {
	return m.resp, nil
}

// benchStreamProvider returns an already-closed channel so a RouteStream
// benchmark drains immediately without blocking.
type benchStreamProvider struct {
	benchProvider
}

func (m *benchStreamProvider) CompleteStream(context.Context, providers.Request) (<-chan providers.StreamChunk, error) {
	ch := make(chan providers.StreamChunk)
	close(ch)
	return ch, nil
}

// okResponse is the canned reply the trivial provider returns.
func okResponse() *providers.Response {
	return &providers.Response{
		Choices: []providers.Choice{{Message: providers.Message{Role: "assistant", Content: "ok"}}},
	}
}

// silenceLogs redirects the gateway's package logger to io.Discard for the
// duration of a benchmark, keeping JSON log lines out of the benchmark output
// (the gateway logger writes to os.Stdout by design).
func silenceLogs(b *testing.B) {
	b.Helper()
	prev := logger.Default()
	logger.SetDefault(logger.New(logger.Options{Output: io.Discard}))
	b.Cleanup(func() { logger.SetDefault(prev) })
}

// newBenchGateway builds a gateway through the public constructor and closes it
// on cleanup.
func newBenchGateway(b *testing.B, cfg config.Config) *aigateway.Gateway {
	b.Helper()
	gw, err := aigateway.New(cfg)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := gw.Close(); err != nil {
			b.Errorf("close gateway: %v", err)
		}
	})
	return gw
}

// BenchmarkGateway_Route measures the overhead of a single Route() call through a
// single-provider configuration with no plugins.
func BenchmarkGateway_Route(b *testing.B) {
	silenceLogs(b)
	gw := newBenchGateway(b, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "bench"}},
	})
	gw.RegisterProvider(&benchProvider{name: "bench", models: []string{"gpt-4o"}, resp: okResponse()})

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
	gw := newBenchGateway(b, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "bench"}},
	})
	gw.RegisterProvider(&benchProvider{name: "bench", models: []string{"gpt-4o"}, resp: okResponse()})

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
	gw := newBenchGateway(b, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "bench-stream"}},
	})
	gw.RegisterProvider(&benchStreamProvider{
		benchProvider: benchProvider{name: "bench-stream", models: []string{"gpt-4o"}},
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
	cfg := config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "bench-plugins"}},
		Plugins: []config.PluginConfig{
			{Name: "word-filter", Enabled: true, Stage: "before_request", Config: map[string]any{"blocked_words": []any{}}},
			{Name: "max-token", Enabled: true, Stage: "before_request", Config: map[string]any{"max_input_tokens": 1000}},
		},
	}
	gw := newBenchGateway(b, cfg)
	gw.RegisterProvider(&benchProvider{name: "bench-plugins", models: []string{"gpt-4o"}, resp: okResponse()})
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

// BenchmarkGateway_RouteWithHook measures Route() with a single async event hook
// registered, and waits for the hook to fire b.N times before returning.
func BenchmarkGateway_RouteWithHook(b *testing.B) {
	silenceLogs(b)
	gw := newBenchGateway(b, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "bench-hook"}},
	})
	gw.RegisterProvider(&benchProvider{name: "bench-hook", models: []string{"gpt-4o"}, resp: okResponse()})

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
	gw := newBenchGateway(b, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "unused"}},
	})
	gw.RegisterProvider(&benchProvider{name: "bench-find", models: []string{"gpt-4o"}, resp: okResponse()})

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
