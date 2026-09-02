package aigateway

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/pkg/circuitbreaker"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// Routing under an open circuit.
//
// The named modes — single, conditional, content-based — commit to one of the
// keys the strategy chose and do not advance past it. Selection was
// breaker-blind, so a mode that commits could commit to a target the gateway had
// already decided not to call. These tests pin the filter that fixes that and —
// just as importantly —
// the fail-open that stops the filter from turning "everything is down" into a
// 404.

// tripBreaker drives a target's circuit to Open. The gateway builds breakers
// lazily for configured targets, so the strategy is asked for one first.
func tripBreaker(t *testing.T, gw *Gateway, key string) {
	t.Helper()
	gw.mu.Lock()
	gw.ensureCircuitBreakersLocked()
	cb := gw.circuitBreakers[key]
	gw.mu.Unlock()
	if cb == nil {
		t.Fatalf("no circuit breaker configured for target %q", key)
	}
	for i := 0; i < 10; i++ {
		cb.RecordFailure()
	}
	if got := cb.State(); got != circuitbreaker.StateOpen {
		t.Fatalf("breaker for %q = %v, want Open", key, got)
	}
}

func openableTarget(key string) config.Target {
	return config.Target{
		VirtualKey:     key,
		Weight:         1,
		CircuitBreaker: &config.CircuitBreakerConfig{FailureThreshold: 1, Timeout: "1h"},
	}
}

func chatReq(model string) providers.Request {
	return providers.Request{
		Model:    model,
		Messages: []providers.Message{{Role: roleUser, Content: "hi"}},
	}
}

// The headline case: least-latency ranks the target it has the best samples
// for, a failed call records no sample, so a target that was fastest and then
// died keeps its low p50 and stays ranked first. Before the health filter this
// mode committed to that dead target and failed every request while a healthy
// sibling sat unused.
func TestRoute_LeastLatency_SkipsOpenCircuitForHealthySibling(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeLatency},
		Targets:  []config.Target{openableTarget("fast"), openableTarget("slow")},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	gw.RegisterProvider(&mockProvider{
		name: "fast", models: []string{"gpt-4o"},
		err: errors.New("upstream is dead"),
	})
	gw.RegisterProvider(&mockProvider{
		name: "slow", models: []string{"gpt-4o"},
		resp: &providers.Response{ID: "served-by-slow"},
	})

	// "fast" was genuinely the fastest, and nothing since has changed that
	// number: its failures recorded no samples at all.
	gw.latencyTracker.Record("fast", "gpt-4o", 1*time.Millisecond)
	gw.latencyTracker.Record("slow", "gpt-4o", 900*time.Millisecond)
	tripBreaker(t, gw, "fast")

	resp, err := gw.Route(context.Background(), chatReq("gpt-4o"))
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if resp.Provider != "slow" {
		t.Fatalf("served by %q, want the healthy sibling %q", resp.Provider, "slow")
	}
}

// The same fix seen through an operator's explicit rule. `conditional` names a
// target on purpose, so the filter must make that target preferred, not
// exclusive: with it open the request still gets served.
func TestRoute_Conditional_SkipsOpenCircuitForHealthySibling(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{
			Mode: config.ModeConditional,
			Conditions: []config.Condition{
				{Key: "model", Value: "gpt-4o", TargetKey: "primary"},
			},
		},
		Targets: []config.Target{openableTarget("primary"), openableTarget("secondary")},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	gw.RegisterProvider(&mockProvider{
		name: "primary", models: []string{"gpt-4o"},
		err: errors.New("upstream is dead"),
	})
	gw.RegisterProvider(&mockProvider{
		name: "secondary", models: []string{"gpt-4o"},
		resp: &providers.Response{ID: "served-by-secondary"},
	})
	tripBreaker(t, gw, "primary")

	resp, err := gw.Route(context.Background(), chatReq("gpt-4o"))
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if resp.Provider != "secondary" {
		t.Fatalf("served by %q, want %q", resp.Provider, "secondary")
	}
}

// The branch the health filter CREATES, and the one that must not be a 404.
//
// A filter that returned an empty list would leave the walk with nothing to
// attempt, and "nothing was attempted" is errNoCapableTarget — 404
// model_not_found, which tells a caller the gateway does not serve a model it
// serves perfectly well. Failing open hands the walk the unfiltered list, the
// breaker refuses it, and ErrCircuitOpen classifies as the 503 it should.
func TestRoute_AllCircuitsOpen_ReportsCircuitOpenNotModelNotFound(t *testing.T) {
	for _, mode := range []config.StrategyMode{
		config.ModeLatency,
		config.ModeLoadBalance,
		config.ModeFallback,
	} {
		t.Run(string(mode), func(t *testing.T) {
			gw, err := newTestGateway(t, config.Config{
				Strategy: config.StrategyConfig{Mode: mode},
				Targets:  []config.Target{openableTarget("a"), openableTarget("b")},
			})
			if err != nil {
				t.Fatalf("new gateway: %v", err)
			}
			gw.RegisterProvider(&mockProvider{name: "a", models: []string{"gpt-4o"}})
			gw.RegisterProvider(&mockProvider{name: "b", models: []string{"gpt-4o"}})
			tripBreaker(t, gw, "a")
			tripBreaker(t, gw, "b")

			_, err = gw.Route(context.Background(), chatReq("gpt-4o"))
			if err == nil {
				t.Fatal("expected a failure with every circuit open")
			}
			// 503: the targets that serve this model are down.
			if !errors.Is(err, circuitbreaker.ErrCircuitOpen) {
				t.Fatalf("error %v does not wrap ErrCircuitOpen (would not classify 503)", err)
			}
			// Not 404: "no target serves this model" is a different answer, and
			// the only one of the two the caller can fix.
			if errors.Is(err, core.ErrNoCapableProvider) {
				t.Fatalf("error %v wraps ErrNoCapableProvider — that classifies 404 model_not_found", err)
			}
		})
	}
}

// A model listing must not move with breaker state. A model that vanished from
// /v1/models while a circuit was open reads as "this gateway does not serve
// that model" and sends a client to change its request over an outage that
// clears itself. routingServes shares eligibleKeys with the walk, so the opt-out
// is the only thing keeping the two apart here.
func TestAllModels_UnchangedWhenCircuitOpens(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeLatency},
		Targets:  []config.Target{openableTarget("a"), openableTarget("b")},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	gw.RegisterProvider(&mockProvider{name: "a", models: []string{"model-a"}})
	gw.RegisterProvider(&mockProvider{name: "b", models: []string{"model-b"}})
	// Samples pin the candidate order least-latency would otherwise shuffle.
	gw.latencyTracker.Record("a", "model-a", 1*time.Millisecond)
	gw.latencyTracker.Record("b", "model-b", 900*time.Millisecond)

	ids := func() []string {
		out := make([]string, 0, 4)
		for _, m := range gw.AllModels() {
			out = append(out, m.ID)
		}
		slices.Sort(out)
		return out
	}

	before := ids()
	if !slices.Equal(before, []string{"model-a", "model-b"}) {
		t.Fatalf("advertised %v before the outage, want both models; the test proves nothing otherwise", before)
	}
	// Exactly ONE target, on purpose. With every circuit open the filter fails
	// open and changes nothing, so an all-open listing would pass whether or not
	// the opt-out exists. One open target is what actually narrows the candidate
	// list, and model-a is then owned by a target the walk would skip.
	tripBreaker(t, gw, "a")

	if after := ids(); !slices.Equal(before, after) {
		t.Fatalf("advertised models changed with circuit state: before %v, after %v", before, after)
	}
}

// A latency window describes a target. Once the target is gone the window means
// nothing, and keeping it leaked for the life of the process — and ranked a
// re-added target on measurements taken before the change.
func TestReloadConfig_DropsLatencySamplesForRemovedTarget(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeLatency},
		Targets:  []config.Target{{VirtualKey: "keep", Weight: 1}, {VirtualKey: "drop", Weight: 1}},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	gw.latencyTracker.Record("keep", "m", 10*time.Millisecond)
	gw.latencyTracker.Record("drop", "m", 20*time.Millisecond)

	if err := gw.ReloadConfig(context.Background(), config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeLatency},
		Targets:  []config.Target{{VirtualKey: "keep", Weight: 1}},
	}); err != nil {
		t.Fatalf("reload: %v", err)
	}

	if gw.latencyTracker.HasSamples("drop") {
		t.Error("samples for the removed target survived the reload")
	}
	if !gw.latencyTracker.HasSamples("keep") {
		t.Error("samples for a surviving target were dropped")
	}
}
