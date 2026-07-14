//go:build integration
// +build integration

// Package strategies_test provides integration tests for gateway routing strategies.
// Tests boot a gateway with multiple stub providers and verify fallback, load
// balance, and least-latency behavior.
package strategies_test

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// miniStub is a minimal Provider for strategy tests.
type miniStub struct {
	name         string
	models       []string
	CompleteFunc func(ctx context.Context, req core.Request) (*core.Response, error)
}

var _ core.Provider = (*miniStub)(nil)

func (s *miniStub) Name() string              { return s.name }
func (s *miniStub) SupportedModels() []string { return s.models }
func (s *miniStub) SupportsModel(m string) bool {
	for _, n := range s.models {
		if n == m {
			return true
		}
	}
	return false
}
func (s *miniStub) Models() []core.ModelInfo { return core.ModelsFromList(s.name, s.models) }
func (s *miniStub) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	if s.CompleteFunc != nil {
		return s.CompleteFunc(ctx, req)
	}
	return &core.Response{
		ID:       s.name + "-resp",
		Object:   "chat.completion",
		Model:    req.Model,
		Provider: s.name,
		Created:  time.Now().Unix(),
		Choices: []core.Choice{
			{Message: core.Message{Role: "assistant", Content: "ok from " + s.name}, FinishReason: "stop"},
		},
	}, nil
}

const stratModel = "strat-model-v1"

// TestStrategy_Fallback_PrimaryFails_SecondarySucceeds verifies that when the
// primary target errors, the fallback strategy tries the secondary and returns
// the secondary's response.
func TestStrategy_Fallback_PrimaryFails_SecondarySucceeds(t *testing.T) {
	primary := &miniStub{
		name:   "primary",
		models: []string{stratModel},
		CompleteFunc: func(_ context.Context, _ core.Request) (*core.Response, error) {
			return nil, fmt.Errorf("provider API error (500): primary down")
		},
	}
	secondary := &miniStub{name: "secondary", models: []string{stratModel}}

	gw, err := aigateway.New(aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeFallback},
		Targets: []aigateway.Target{
			{VirtualKey: "primary"},
			{VirtualKey: "secondary"},
		},
	})
	if err != nil {
		t.Fatalf("aigateway.New: %v", err)
	}
	gw.RegisterProvider(primary)
	gw.RegisterProvider(secondary)

	resp, err := gw.Route(t.Context(), core.Request{
		Model:    stratModel,
		Messages: []core.Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("expected success from secondary, got error: %v", err)
	}
	if resp.Provider != "secondary" {
		t.Errorf("response.Provider = %q; want %q", resp.Provider, "secondary")
	}
	if len(resp.Choices) == 0 || !strings.Contains(resp.Choices[0].Message.Content, "secondary") {
		t.Errorf("response content %q does not mention secondary", resp.Choices[0].Message.Content)
	}
}

// TestStrategy_Fallback_AllFail returns an error wrapping all provider failures.
func TestStrategy_Fallback_AllFail(t *testing.T) {
	gw, err := aigateway.New(aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeFallback},
		Targets: []aigateway.Target{
			{VirtualKey: "p1"},
			{VirtualKey: "p2"},
		},
	})
	if err != nil {
		t.Fatalf("aigateway.New: %v", err)
	}
	for _, name := range []string{"p1", "p2"} {
		n := name
		gw.RegisterProvider(&miniStub{
			name:   n,
			models: []string{stratModel},
			CompleteFunc: func(_ context.Context, _ core.Request) (*core.Response, error) {
				return nil, fmt.Errorf("provider API error (500): %s failed", n)
			},
		})
	}

	_, err = gw.Route(t.Context(), core.Request{
		Model:    stratModel,
		Messages: []core.Message{{Role: "user", Content: "hello"}},
	})
	if err == nil {
		t.Fatal("expected error when all providers fail, got nil")
	}
	if !strings.Contains(err.Error(), "all providers failed") {
		t.Errorf("error %q should mention all providers failed", err.Error())
	}
}

// TestStrategy_LoadBalance_DistributesRequests verifies that with two equal-weight
// targets, requests are distributed across both providers (not always the same one).
func TestStrategy_LoadBalance_DistributesRequests(t *testing.T) {
	var p1Count, p2Count atomic.Int64
	makeProvider := func(name string, counter *atomic.Int64) *miniStub {
		return &miniStub{
			name:   name,
			models: []string{stratModel},
			CompleteFunc: func(_ context.Context, req core.Request) (*core.Response, error) {
				counter.Add(1)
				return &core.Response{ID: name + "-resp", Object: "chat.completion", Model: req.Model,
					Provider: name,
					Choices:  []core.Choice{{Message: core.Message{Role: "assistant", Content: "ok"}, FinishReason: "stop"}}}, nil
			},
		}
	}

	gw, err := aigateway.New(aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeLoadBalance},
		Targets: []aigateway.Target{
			{VirtualKey: "lb1", Weight: 1.0},
			{VirtualKey: "lb2", Weight: 1.0},
		},
	})
	if err != nil {
		t.Fatalf("aigateway.New: %v", err)
	}
	gw.RegisterProvider(makeProvider("lb1", &p1Count))
	gw.RegisterProvider(makeProvider("lb2", &p2Count))

	const n = 40
	for i := range n {
		if _, err := gw.Route(t.Context(), core.Request{
			Model:    stratModel,
			Messages: []core.Message{{Role: "user", Content: "req"}},
		}); err != nil {
			t.Fatalf("request %d failed: %v", i, err)
		}
	}

	c1, c2 := p1Count.Load(), p2Count.Load()
	t.Logf("load balance: lb1=%d lb2=%d (total=%d)", c1, c2, c1+c2)

	if c1+c2 != n {
		t.Errorf("total calls = %d; want %d", c1+c2, n)
	}
	// Both providers should have handled at least 20% of requests.
	minExpected := int64(float64(n) * 0.20)
	if c1 < minExpected || c2 < minExpected {
		t.Errorf("load unbalanced: lb1=%d lb2=%d; each should have >= %d calls", c1, c2, minExpected)
	}
}

// TestStrategy_LeastLatency_LocksOntoFastestSeen verifies the key contract of
// the least-latency strategy: once the tracker has observed both providers, all
// subsequent requests are routed to the faster one (no random switching).
//
// The test seeds the tracker by running requests until both providers have been
// seen at least once (the strategy picks randomly on cold-start with no samples).
// After that, it asserts the fast provider handles >= 80% of requests.
func TestStrategy_LeastLatency_LocksOntoFastestSeen(t *testing.T) {
	var fastTotal, slowTotal atomic.Int64

	fast := &miniStub{
		name:   "fast",
		models: []string{stratModel},
		CompleteFunc: func(_ context.Context, req core.Request) (*core.Response, error) {
			fastTotal.Add(1)
			return &core.Response{ID: "fast-resp", Object: "chat.completion", Model: req.Model,
				Provider: "fast",
				Choices:  []core.Choice{{Message: core.Message{Role: "assistant", Content: "ok"}, FinishReason: "stop"}}}, nil
		},
	}
	slow := &miniStub{
		name:   "slow",
		models: []string{stratModel},
		CompleteFunc: func(_ context.Context, req core.Request) (*core.Response, error) {
			slowTotal.Add(1)
			time.Sleep(50 * time.Millisecond)
			return &core.Response{ID: "slow-resp", Object: "chat.completion", Model: req.Model,
				Provider: "slow",
				Choices:  []core.Choice{{Message: core.Message{Role: "assistant", Content: "ok"}, FinishReason: "stop"}}}, nil
		},
	}

	gw, err := aigateway.New(aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeLatency},
		Targets: []aigateway.Target{
			{VirtualKey: "fast"},
			{VirtualKey: "slow"},
		},
	})
	if err != nil {
		t.Fatalf("aigateway.New: %v", err)
	}
	gw.RegisterProvider(fast)
	gw.RegisterProvider(slow)

	req := core.Request{
		Model:    stratModel,
		Messages: []core.Message{{Role: "user", Content: "req"}},
	}

	// Seed deterministically: the least-latency strategy always routes to an
	// as-yet-unseen provider before committing to a best-known one, so exactly one
	// request per target profiles every provider — randomness affects only the
	// order the two are first sampled, never coverage. Two requests therefore
	// guarantee both "fast" and "slow" have a latency sample.
	const seedRequests = 2 // == number of targets
	for i := range seedRequests {
		if _, err := gw.Route(t.Context(), req); err != nil {
			t.Fatalf("seed request %d failed: %v", i, err)
		}
	}
	if fastTotal.Load() == 0 || slowTotal.Load() == 0 {
		t.Fatalf("seeding failed to sample both providers: fast=%d slow=%d",
			fastTotal.Load(), slowTotal.Load())
	}

	// After seeding, the tracker has accurate latency for both providers.
	// Reset counters and measure steady-state routing preference.
	fastTotal.Store(0)
	slowTotal.Store(0)

	const n = 20
	for i := range n {
		if _, err := gw.Route(t.Context(), req); err != nil {
			t.Fatalf("measurement request %d failed: %v", i, err)
		}
	}

	fc, sc := fastTotal.Load(), slowTotal.Load()
	t.Logf("post-seed routing: fast=%d slow=%d (total=%d)", fc, sc, fc+sc)

	// Fast should handle >= 80% of requests since it has measurably lower latency.
	if fc < int64(float64(n)*0.80) {
		t.Errorf("expected fast provider to handle >= 80%% of requests: fast=%d slow=%d", fc, sc)
	}
}
