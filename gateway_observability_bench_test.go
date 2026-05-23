package aigateway

import (
	"context"
	"testing"

	"github.com/ferro-labs/ai-gateway/observability"
	"github.com/ferro-labs/ai-gateway/providers"
)

// BenchmarkRoute_TracingOff measures hot-path allocations when observability
// is disabled (NoOp provider). Use this for allocation trend tracking over
// time. The assertion that installing NoOp() explicitly adds zero allocations
// versus the default gateway is enforced by TestRoute_TracingOff_AllocBaseline.
//
// Run with:
//
//	go test -run=NONE -bench=BenchmarkRoute_TracingOff -benchmem
func BenchmarkRoute_TracingOff(b *testing.B) {
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "mock"}},
	})
	gw.SetObservability(observability.NoOp())

	gw.RegisterProvider(&mockProvider{
		name:   "mock",
		models: []string{"gpt-4o"},
		resp: &providers.Response{
			ID:       "r1",
			Provider: "mock",
			Model:    "gpt-4o",
			Usage:    providers.Usage{PromptTokens: 5, CompletionTokens: 5},
		},
	})

	req := providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	}
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = gw.Route(ctx, req)
	}
}

// TestRoute_TracingOff_AllocBaseline asserts the issue #49 acceptance
// criterion: calling SetObservability(observability.NoOp()) must add ZERO
// allocations compared to a gateway that never calls SetObservability (which
// already uses NoOp internally as its default). Both paths must hit the same
// code, so their per-operation allocation counts must be identical.
//
// The test uses testing.AllocsPerRun over 200 iterations after a warm-up
// call to drain any one-time lazy-init allocations (e.g. sync.Once, map
// creation) that would otherwise skew the measurement. It asserts parity
// (noopAllocs <= defaultAllocs) rather than an absolute zero, because the
// absolute count legitimately varies with the mock provider overhead and
// can differ by sub-1.0 fractions due to AllocsPerRun averaging.
func TestRoute_TracingOff_AllocBaseline(t *testing.T) {
	resp := &providers.Response{
		ID:       "r1",
		Provider: "mock",
		Model:    "gpt-4o",
		Usage:    providers.Usage{PromptTokens: 5, CompletionTokens: 5},
	}
	req := providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	}
	ctx := context.Background()

	// gwDefault uses the internal NoOp default — SetObservability never called.
	gwDefault, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "mock"}},
	})
	gwDefault.RegisterProvider(&mockProvider{
		name:   "mock",
		models: []string{"gpt-4o"},
		resp:   resp,
	})

	// gwNoOp installs NoOp explicitly via SetObservability.
	gwNoOp, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "mock"}},
	})
	gwNoOp.SetObservability(observability.NoOp())
	gwNoOp.RegisterProvider(&mockProvider{
		name:   "mock",
		models: []string{"gpt-4o"},
		resp:   resp,
	})

	// Warm up each gateway once to flush any sync.Once / lazy-init paths.
	_, _ = gwDefault.Route(ctx, req)
	_, _ = gwNoOp.Route(ctx, req)

	defaultAllocs := testing.AllocsPerRun(200, func() {
		_, _ = gwDefault.Route(ctx, req)
	})
	noopAllocs := testing.AllocsPerRun(200, func() {
		_, _ = gwNoOp.Route(ctx, req)
	})

	if noopAllocs > defaultAllocs {
		t.Errorf("installing NoOp() added allocations vs the default gateway: default=%v noop=%v", defaultAllocs, noopAllocs)
	}
}
