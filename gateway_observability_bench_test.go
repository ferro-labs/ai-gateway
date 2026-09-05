package aigateway

import (
	"context"
	"errors"
	"testing"

	"github.com/ferro-labs/ai-gateway/config"
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
	gw, _ := newTestGateway(b, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "mock"}},
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

func TestRoutingAttemptRecordingDisabledAllocatesNothing(t *testing.T) {
	gw, _ := newTestGateway(t, config.Config{})
	err := errors.New("account@example.com AKIAIOSFODNN7EXAMPLE")
	allocs := testing.AllocsPerRun(1000, func() {
		gw.recordRoutingAttempt(context.Background(), observability.NoOp(), false, routingAttempt{}, 0, err)
	})
	if allocs != 0 {
		t.Fatalf("disabled routing-attempt recording allocated %v times per call, want 0", allocs)
	}
}

func TestAttemptTargetDisabledRecordingAllocatesNothing(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "mock"}},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	ctx := context.Background()
	p := &mockProvider{name: "mock", models: []string{"gpt-4o"}}
	resp := &providers.Response{ID: "ok", Provider: "mock", Model: "gpt-4o"}
	call := func(context.Context, providers.Provider, providers.Request, string) (*providers.Response, error) {
		return resp, nil
	}
	req := providers.Request{Model: "gpt-4o"}
	target := routedTarget{key: "mock", priceProvider: "mock", upstreamModel: "gpt-4o"}
	sequence := 0

	callAllocs := testing.AllocsPerRun(1000, func() {
		_, _ = callUnderResilience(ctx, "mock", p, nil, nil, req, "gpt-4o", call)
	})
	attemptAllocs := testing.AllocsPerRun(1000, func() {
		sequence = 0
		_, _ = attemptTarget(ctx, gw, observability.NoOp(), false, nil, target, "gpt-4o", &sequence, p, nil, nil, req, "gpt-4o", call)
	})
	t.Logf("allocations per run: attempt=%v call=%v attributable=%v", attemptAllocs, callAllocs, attemptAllocs-callAllocs)
	if attributable := attemptAllocs - callAllocs; attributable >= 1 {
		t.Fatalf("disabled attempt recording allocations = %v (attempt=%v call=%v), want 0", attributable, attemptAllocs, callAllocs)
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
	gwDefault, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "mock"}},
	})

	gwDefault.RegisterProvider(&mockProvider{
		name:   "mock",
		models: []string{"gpt-4o"},
		resp:   resp,
	})

	// gwNoOp installs NoOp explicitly via SetObservability.
	gwNoOp, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "mock"}},
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
