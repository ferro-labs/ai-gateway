package aigateway

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/pkg/circuitbreaker"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// rateLimitedOnce answers 429 (with the given Retry-After) on its first call
// and succeeds afterwards, so a test can tell "parked" from "recovered".
func rateLimitedOnce(name string, retryAfter time.Duration) (*mockProvider, *atomic.Int32) {
	var calls atomic.Int32
	p := &mockProvider{name: name, models: []string{pipelineModel}, completeFn: func(context.Context, providers.Request) (*providers.Response, error) {
		if calls.Add(1) == 1 {
			err := core.StatusError(name, http.StatusTooManyRequests, "slow down")
			err.RetryAfter = retryAfter
			return nil, err
		}
		return &providers.Response{ID: "served-by-" + name, Model: pipelineModel}, nil
	}}
	return p, &calls
}

func cooldownGateway(t *testing.T, targets []config.Target) (*Gateway, *time.Time) {
	t.Helper()
	gw, err := newTestGateway(t, config.Config{Strategy: config.StrategyConfig{Mode: config.ModeFallback}, Targets: targets})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	gw.now = func() time.Time { return now }
	return gw, &now
}

func routeServedBy(t *testing.T, gw *Gateway) string {
	t.Helper()
	resp, err := gw.Route(context.Background(), pipelineRequest())
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	return resp.ID
}

// A target that answered 429 is parked for its Retry-After: the next request
// does not pay another 429 on it, and it is offered traffic again once the
// hint has elapsed.
func TestPipeline_RateLimitedTargetIsParkedForRetryAfter(t *testing.T) {
	limited, calls := rateLimitedOnce("limited", 2*time.Second)
	gw, now := cooldownGateway(t, []config.Target{{VirtualKey: "limited"}, {VirtualKey: "sibling"}})
	gw.RegisterProvider(limited)
	gw.RegisterProvider(&mockProvider{name: "sibling", models: []string{pipelineModel}, resp: &providers.Response{ID: "served-by-sibling"}})

	if got := routeServedBy(t, gw); got != "served-by-sibling" {
		t.Fatalf("first request served by %q, want the sibling after the 429", got)
	}
	if got := routeServedBy(t, gw); got != "served-by-sibling" || calls.Load() != 1 {
		t.Fatalf("second request served by %q with %d calls to the limited target; it must be parked", got, calls.Load())
	}
	*now = now.Add(2*time.Second + time.Millisecond)
	if got := routeServedBy(t, gw); got != "served-by-limited" {
		t.Fatalf("after Retry-After elapsed served by %q, want the limited target back in rotation", got)
	}
}

// Without a usable Retry-After the park is the bounded default; with an
// excessive one it is capped, so a target cannot be parked for an hour on
// one header.
func TestPipeline_RateLimitCooldownIsBounded(t *testing.T) {
	for _, tc := range []struct {
		name       string
		retryAfter time.Duration
		parkedFor  time.Duration
	}{
		{"absent hint parks for the default", 0, defaultRateLimitCooldown},
		{"excessive hint is capped", time.Hour, maxRateLimitCooldown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			limited, _ := rateLimitedOnce("limited", tc.retryAfter)
			gw, now := cooldownGateway(t, []config.Target{{VirtualKey: "limited"}, {VirtualKey: "sibling"}})
			gw.RegisterProvider(limited)
			gw.RegisterProvider(&mockProvider{name: "sibling", models: []string{pipelineModel}, resp: &providers.Response{ID: "served-by-sibling"}})

			routeServedBy(t, gw)
			*now = now.Add(tc.parkedFor - time.Millisecond)
			if got := routeServedBy(t, gw); got != "served-by-sibling" {
				t.Fatalf("served by %q just before the park ends, want the sibling", got)
			}
			*now = now.Add(2 * time.Millisecond)
			if got := routeServedBy(t, gw); got != "served-by-limited" {
				t.Fatalf("served by %q after the park, want the limited target", got)
			}
		})
	}
}

// A 429 parks the target; it does not count against its circuit breaker. The
// two mean different things — "come back later" versus "this target is
// failing" — and a rate limit must not open the circuit on a healthy target.
func TestPipeline_RateLimitParksWithoutTrippingTheBreaker(t *testing.T) {
	limited, _ := rateLimitedOnce("limited", time.Second)
	gw, _ := cooldownGateway(t, []config.Target{
		{VirtualKey: "limited", CircuitBreaker: &config.CircuitBreakerConfig{FailureThreshold: 1, SuccessThreshold: 1, MaxHalfThreshold: 1, Timeout: "1m"}},
		{VirtualKey: "sibling"},
	})
	gw.RegisterProvider(limited)
	gw.RegisterProvider(&mockProvider{name: "sibling", models: []string{pipelineModel}, resp: &providers.Response{ID: "served-by-sibling"}})

	routeServedBy(t, gw)
	gw.mu.RLock()
	state := gw.circuitBreakers["limited"].State()
	gw.mu.RUnlock()
	if state != circuitbreaker.StateClosed {
		t.Fatalf("breaker state = %v after a 429, want closed", state)
	}
}

// Parking filters; it does not refuse. When every target that serves the
// model is parked the request is still attempted, exactly as it is when every
// circuit is open — "everything is throttled" is not "nothing serves this".
func TestPipeline_EveryTargetParkedStillAttempts(t *testing.T) {
	limited, calls := rateLimitedOnce("limited", time.Minute)
	gw, _ := cooldownGateway(t, []config.Target{{VirtualKey: "limited"}})
	gw.RegisterProvider(limited)

	if _, err := gw.Route(context.Background(), pipelineRequest()); err == nil {
		t.Fatal("first request succeeded, want the 429")
	}
	if got := routeServedBy(t, gw); got != "served-by-limited" || calls.Load() != 2 {
		t.Fatalf("second request: served by %q after %d calls; the only target must still be attempted", got, calls.Load())
	}
}
