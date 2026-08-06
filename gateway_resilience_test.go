package aigateway

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/pkg/circuitbreaker"
	"github.com/ferro-labs/ai-gateway/providers"
)

// resilienceFor reads a target's live breaker and limiter under the same lock
// the gateway writes them through.
func resilienceFor(t *testing.T, gw *Gateway, key string) (*circuitbreaker.CircuitBreaker, *providerLimiter) {
	t.Helper()
	gw.mu.RLock()
	defer gw.mu.RUnlock()
	return gw.circuitBreakers[key], gw.limiters[key]
}

// TestGateway_RouteStream_AbandonedStartReleasesHalfOpenProbe covers the start
// that outruns request_timeout and then succeeds anyway. Nobody downstream ever
// sees its channel, so the half-open probe it holds is resolved by the goroutine
// that abandons it or by nothing at all — and a probe left at the cap makes
// Allow() reject the target for the life of the process.
func TestGateway_RouteStream_AbandonedStartReleasesHalfOpenProbe(t *testing.T) {
	var calls atomic.Int32
	unblock := make(chan struct{})
	drained := make(chan struct{})

	gw, err := newTestGateway(t, config.Config{
		Strategy:       config.StrategyConfig{Mode: config.ModeSingle},
		RequestTimeout: "50ms",
		Targets: []config.Target{{
			VirtualKey: "slow-start",
			CircuitBreaker: &config.CircuitBreakerConfig{
				FailureThreshold: 1,
				SuccessThreshold: 1,
				MaxHalfThreshold: 1,
				Timeout:          "1ms",
			},
		}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: "slow-start", models: []string{"gpt-4o"}},
		streamFn: func(context.Context, providers.Request) (<-chan providers.StreamChunk, error) {
			switch calls.Add(1) {
			case 1:
				return nil, errors.New("stream startup failed")
			case 2:
				// Outlast the start deadline so the attempt is abandoned, then
				// succeed. The unbuffered chunk is only delivered once the
				// abandoning goroutine drains, which orders `drained` after
				// whatever probe bookkeeping that goroutine does first.
				<-unblock
				ch := make(chan providers.StreamChunk)
				go func() {
					ch <- providers.StreamChunk{ID: "abandoned"}
					close(ch)
					close(drained)
				}()
				return ch, nil
			default:
				ch := make(chan providers.StreamChunk)
				close(ch)
				return ch, nil
			}
		},
	})

	cb, _ := resilienceFor(t, gw, "slow-start")
	if cb == nil {
		t.Fatal("expected a circuit breaker for slow-start")
	}
	fakeNow := time.Unix(0, 0)
	cb.SetNowForTest(func() time.Time { return fakeNow })

	req := streamTestRequest()

	// Trip the breaker, then let its open timeout elapse so the next request is
	// the single half-open probe.
	if _, err := gw.RouteStream(context.Background(), req); err == nil {
		t.Fatal("expected the first stream start to fail and open the breaker")
	}
	if cb.State() != circuitbreaker.StateOpen {
		t.Fatalf("breaker state = %v, want open", cb.State())
	}
	fakeNow = fakeNow.Add(5 * time.Millisecond)

	// The probe overruns request_timeout and is abandoned mid-start.
	if _, err := gw.RouteStream(context.Background(), req); err == nil {
		t.Fatal("expected the abandoned stream start to be reported as a failure")
	}
	close(unblock)
	<-drained

	ch, err := gw.RouteStream(context.Background(), req)
	if err != nil {
		t.Fatalf("request after an abandoned start = %v, want success: the abandoned probe was never released", err)
	}
	drainMeteredStream(t, ch)
	if got := calls.Load(); got != 3 {
		t.Fatalf("CompleteStream calls = %d, want 3", got)
	}
}

// TestGateway_Route_QueuedShedDoesNotBlameProvider covers a request the gateway
// turned away itself: it expired while queued behind the target's own
// concurrency limit and never reached the provider, so it must not count toward
// opening that provider's breaker.
func TestGateway_Route_QueuedShedDoesNotBlameProvider(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy:       config.StrategyConfig{Mode: config.ModeSingle},
		RequestTimeout: "20ms",
		Targets: []config.Target{{
			VirtualKey: mockProviderName,
			CircuitBreaker: &config.CircuitBreakerConfig{
				FailureThreshold: 1,
				SuccessThreshold: 1,
				MaxHalfThreshold: 1,
				Timeout:          "1m",
			},
			Concurrency: &config.ConcurrencyConfig{MaxConcurrency: 1, QueueSize: 10},
		}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.RegisterProvider(&mockProvider{
		name:   mockProviderName,
		models: []string{"gpt-4o"},
		resp:   &providers.Response{ID: "ok"},
	})

	cb, lim := resilienceFor(t, gw, mockProviderName)
	if cb == nil || lim == nil {
		t.Fatal("expected a circuit breaker and a limiter for the target")
	}

	// Hold the only in-flight slot without calling the provider, so the request
	// under test is the sole outcome the breaker could record.
	if err := lim.acquire(context.Background()); err != nil {
		t.Fatalf("hold the in-flight slot: %v", err)
	}
	defer lim.release()

	_, err = gw.Route(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: roleUser, Content: "hi"}},
	})
	if !errors.Is(err, providers.ErrProviderSaturated) {
		t.Errorf("queued shed = %v, want ErrProviderSaturated", err)
	}
	if cb.State() != circuitbreaker.StateClosed {
		t.Fatalf("breaker state = %v, want closed: shedding under our own concurrency limit is not the provider's fault", cb.State())
	}
}

// TestGateway_ReloadConfig_CarriesLiveResilienceState covers what a reload may
// and may not forget: an open circuit and an occupied limiter must survive a
// config change that did not touch them, while a changed block must produce a
// fresh instance so the operator's new setting actually takes effect.
func TestGateway_ReloadConfig_CarriesLiveResilienceState(t *testing.T) {
	target := config.Target{
		VirtualKey: mockProviderName,
		CircuitBreaker: &config.CircuitBreakerConfig{
			FailureThreshold: 1,
			SuccessThreshold: 1,
			MaxHalfThreshold: 1,
			Timeout:          "1m",
		},
		Concurrency: &config.ConcurrencyConfig{MaxConcurrency: 2, QueueSize: 4},
	}
	cfg := config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{target},
	}
	gw, err := newTestGateway(t, cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	cb, lim := resilienceFor(t, gw, mockProviderName)
	if cb == nil || lim == nil {
		t.Fatal("expected a circuit breaker and a limiter for the target")
	}
	cb.RecordFailure() // threshold 1 → open

	if err := gw.ReloadConfig(context.Background(), cfg); err != nil {
		t.Fatalf("ReloadConfig: %v", err)
	}
	keptCB, keptLim := resilienceFor(t, gw, mockProviderName)
	if keptCB != cb {
		t.Error("a reload that did not change circuit_breaker must keep the live breaker")
	}
	if keptLim != lim {
		t.Error("a reload that did not change concurrency must keep the live limiter")
	}
	if keptCB.State() != circuitbreaker.StateOpen {
		t.Errorf("breaker state = %v, want open: a reload must not re-admit traffic to a failing provider", keptCB.State())
	}

	// A changed block must be rebuilt, or the new setting is silently ignored.
	changedCB := *target.CircuitBreaker
	changedCB.Timeout = "2m"
	changedConc := *target.Concurrency
	changedConc.MaxConcurrency = 3
	changed := cfg
	changed.Targets = []config.Target{{
		VirtualKey:     mockProviderName,
		CircuitBreaker: &changedCB,
		Concurrency:    &changedConc,
	}}
	if err := gw.ReloadConfig(context.Background(), changed); err != nil {
		t.Fatalf("ReloadConfig with changed settings: %v", err)
	}
	freshCB, freshLim := resilienceFor(t, gw, mockProviderName)
	if freshCB == cb {
		t.Error("a changed circuit_breaker block must produce a fresh breaker")
	}
	if freshLim == lim {
		t.Error("a changed concurrency block must produce a fresh limiter")
	}
	if freshCB.State() != circuitbreaker.StateClosed {
		t.Errorf("rebuilt breaker state = %v, want closed", freshCB.State())
	}
	if cap(freshLim.slots) != changedConc.MaxConcurrency {
		t.Errorf("rebuilt limiter capacity = %d, want %d", cap(freshLim.slots), changedConc.MaxConcurrency)
	}

	// A target the config dropped must leave nothing behind.
	dropped := cfg
	dropped.Targets = []config.Target{{VirtualKey: "other"}}
	if err := gw.ReloadConfig(context.Background(), dropped); err != nil {
		t.Fatalf("ReloadConfig dropping the target: %v", err)
	}
	if goneCB, goneLim := resilienceFor(t, gw, mockProviderName); goneCB != nil || goneLim != nil {
		t.Errorf("dropped target kept breaker=%v limiter=%v, want both removed", goneCB, goneLim)
	}
}
