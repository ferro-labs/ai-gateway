package aigateway

import (
	"context"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/events"
	"github.com/ferro-labs/ai-gateway/internal/metrics"
	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/providers"
)

func TestGateway_PublishEvent_NoHooks(_ *testing.T) {
	gw := &Gateway{
		hooks: newHookBus(1),
	}

	gw.publishEvent(context.Background(), completedHookEvent("no-hooks"))
}

func TestGateway_Route_ProviderNotFound(t *testing.T) {
	gw, _ := newTestGateway(t, Config{
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
	gw, _ := newTestGateway(t, Config{
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
	gw, err := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "unused"}},
	})

	if err != nil {
		t.Fatal(err)
	}

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
		hooks: newHookBus(2),
	}

	gw.AddHook(func(context.Context, string, map[string]any) {})
	gw.AddHook(func(context.Context, string, map[string]any) {})

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

	if got := len(gw.hooks.dispatchQ); got != 2 {
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
		hooks: newHookBus(1),
	}
	gw.AddHook(func(context.Context, string, map[string]any) {})

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
