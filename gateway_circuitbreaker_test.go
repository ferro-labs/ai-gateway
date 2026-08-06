package aigateway

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/pkg/circuitbreaker"
	"github.com/ferro-labs/ai-gateway/pkg/metrics"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

func TestGateway_RouteStream_ImmediateFailure_IncrementsProviderErrors(t *testing.T) {
	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "mock-stream"}},
	})

	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{
			name:   "mock-stream",
			models: []string{"gpt-4o"},
		},
		streamErr: errors.New("stream failed"),
	})

	beforeReq := counterValue(t, metrics.RequestsTotal.WithLabelValues("mock-stream", "gpt-4o", "error"))
	beforeProvErr := counterValue(t, metrics.ProviderErrors.WithLabelValues("mock-stream", "provider_error"))

	_, err := gw.RouteStream(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected stream startup error")
	}

	afterReq := counterValue(t, metrics.RequestsTotal.WithLabelValues("mock-stream", "gpt-4o", "error"))
	afterProvErr := counterValue(t, metrics.ProviderErrors.WithLabelValues("mock-stream", "provider_error"))
	if afterReq-beforeReq != 1 {
		t.Fatalf("gateway_requests_total error delta = %v, want 1", afterReq-beforeReq)
	}
	if afterProvErr-beforeProvErr != 1 {
		t.Fatalf("gateway_provider_errors_total provider_error delta = %v, want 1", afterProvErr-beforeProvErr)
	}
}

func TestGateway_RouteStream_ImmediateCircuitOpen_IncrementsCircuitOpenProviderErrors(t *testing.T) {
	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "mock-stream"}},
	})

	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{
			name:   "mock-stream",
			models: []string{"gpt-4o"},
		},
		streamErr: circuitbreaker.ErrCircuitOpen,
	})

	beforeReq := counterValue(t, metrics.RequestsTotal.WithLabelValues("mock-stream", "gpt-4o", "error"))
	beforeProvErr := counterValue(t, metrics.ProviderErrors.WithLabelValues("mock-stream", "circuit_open"))

	_, err := gw.RouteStream(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected circuit-open stream startup error")
	}

	afterReq := counterValue(t, metrics.RequestsTotal.WithLabelValues("mock-stream", "gpt-4o", "error"))
	afterProvErr := counterValue(t, metrics.ProviderErrors.WithLabelValues("mock-stream", "circuit_open"))
	if afterReq-beforeReq != 1 {
		t.Fatalf("gateway_requests_total error delta = %v, want 1", afterReq-beforeReq)
	}
	if afterProvErr-beforeProvErr != 1 {
		t.Fatalf("gateway_provider_errors_total circuit_open delta = %v, want 1", afterProvErr-beforeProvErr)
	}
}

func streamTestRequest() providers.Request {
	return providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	}
}

func drainMeteredStream(t *testing.T, ch <-chan providers.StreamChunk) {
	t.Helper()
	for range ch { //nolint:revive // empty-block: intentionally draining the stream to completion
	}
}

func TestGateway_RouteStream_StartupFailureTripsCircuitWithoutRoute(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets: []config.Target{{
			VirtualKey: "flaky-stream",
			CircuitBreaker: &config.CircuitBreakerConfig{
				FailureThreshold: 2,
			},
		}},
	})

	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{
			name:   "flaky-stream",
			models: []string{"gpt-4o"},
		},
		streamErr: errors.New("stream startup failed"),
	})

	req := streamTestRequest()
	for i := 0; i < 2; i++ {
		_, err := gw.RouteStream(context.Background(), req)
		if err == nil {
			t.Fatalf("attempt %d: expected startup error", i+1)
		}
	}

	_, err = gw.RouteStream(context.Background(), req)
	if !errors.Is(err, circuitbreaker.ErrCircuitOpen) {
		t.Fatalf("expected circuit open, got %v", err)
	}
}

func TestGateway_RouteStream_FallbackSkipsNonStreamingTargetWithCircuitBreaker(t *testing.T) {
	var selected atomic.Value
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeFallback},
		Targets: []config.Target{
			{
				VirtualKey: "plain",
				CircuitBreaker: &config.CircuitBreakerConfig{
					FailureThreshold: 2,
				},
			},
			{VirtualKey: "stream"},
		},
	})

	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.RegisterProvider(&mockProvider{
		name:   "plain",
		models: []string{"gpt-4o"},
	})
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{
			name:   "stream",
			models: []string{"gpt-4o"},
		},
		streamFn: func(_ context.Context, _ providers.Request) (<-chan providers.StreamChunk, error) {
			selected.Store("stream")
			ch := make(chan providers.StreamChunk, 1)
			ch <- providers.StreamChunk{
				ID: "stream-ok",
				Choices: []providers.StreamChoice{{
					Delta: providers.MessageDelta{Content: "ok"},
				}},
			}
			close(ch)
			return ch, nil
		},
	})

	ch, err := gw.RouteStream(context.Background(), streamTestRequest())
	if err != nil {
		t.Fatalf("RouteStream error = %v, want streaming fallback target", err)
	}
	drainMeteredStream(t, ch)
	if got := selected.Load(); got != "stream" {
		t.Fatalf("selected provider = %v, want stream", got)
	}
}

func TestGateway_RouteStream_StartupCancellationDoesNotTripCircuit(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets: []config.Target{{
			VirtualKey: "flaky-stream",
			CircuitBreaker: &config.CircuitBreakerConfig{
				FailureThreshold: 2,
			},
		}},
	})

	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{
			name:   "flaky-stream",
			models: []string{"gpt-4o"},
		},
		streamErr: context.Canceled,
	})

	req := streamTestRequest()
	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := gw.RouteStream(ctx, req)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("attempt %d: error = %v, want context.Canceled", i+1, err)
		}
	}

	gw.mu.RLock()
	cb := gw.circuitBreakers["flaky-stream"]
	gw.mu.RUnlock()
	if cb == nil {
		t.Fatal("expected circuit breaker for flaky-stream")
	}
	if cb.State() != circuitbreaker.StateClosed {
		t.Fatalf("circuit state = %v, want closed after startup cancellations", cb.State())
	}
}

func TestGateway_Route_ProviderTimeoutTripsCircuit(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets: []config.Target{{
			VirtualKey: "slow",
			CircuitBreaker: &config.CircuitBreakerConfig{
				FailureThreshold: 2,
			},
		}},
	})

	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.RegisterProvider(&mockProvider{
		name:   "slow",
		models: []string{"gpt-4o"},
		err:    context.DeadlineExceeded,
	})

	req := providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	}
	for i := 0; i < 2; i++ {
		_, routeErr := gw.Route(context.Background(), req)
		if !errors.Is(routeErr, context.DeadlineExceeded) {
			t.Fatalf("attempt %d: error = %v, want context.DeadlineExceeded", i+1, routeErr)
		}
	}

	_, routeErr := gw.Route(context.Background(), req)
	if !errors.Is(routeErr, circuitbreaker.ErrCircuitOpen) {
		t.Fatalf("expected circuit open after provider timeouts, got %v", routeErr)
	}
}

func TestGateway_RouteStream_ProviderTimeoutTripsCircuit(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets: []config.Target{{
			VirtualKey: "flaky-stream",
			CircuitBreaker: &config.CircuitBreakerConfig{
				FailureThreshold: 2,
			},
		}},
	})

	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{
			name:   "flaky-stream",
			models: []string{"gpt-4o"},
		},
		streamErr: context.DeadlineExceeded,
	})

	req := streamTestRequest()
	for i := 0; i < 2; i++ {
		_, err := gw.RouteStream(context.Background(), req)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("attempt %d: error = %v, want context.DeadlineExceeded", i+1, err)
		}
	}

	_, err = gw.RouteStream(context.Background(), req)
	if !errors.Is(err, circuitbreaker.ErrCircuitOpen) {
		t.Fatalf("expected circuit open after provider timeouts, got %v", err)
	}
}

// TestGateway_RouteStream_HalfOpenAllowsSingleProbe guards against a resolve-time
// double probe: the stream provider resolver must not consume the half-open
// permit, so the recovering provider's single probe reaches CompleteStream and
// succeeds. An open circuit (timeout not elapsed) must still be skipped.
func TestGateway_RouteStream_HalfOpenAllowsSingleProbe(t *testing.T) {
	var streamCalls atomic.Int32
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets: []config.Target{{
			VirtualKey: "recovering-stream",
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
		mockProvider: mockProvider{
			name:   "recovering-stream",
			models: []string{"gpt-4o"},
		},
		streamFn: func(_ context.Context, _ providers.Request) (<-chan providers.StreamChunk, error) {
			if streamCalls.Add(1) == 1 {
				return nil, errors.New("stream startup failed")
			}
			ch := make(chan providers.StreamChunk, 1)
			ch <- providers.StreamChunk{
				ID:      "recovered",
				Choices: []providers.StreamChoice{{Delta: providers.MessageDelta{Content: "ok"}}},
			}
			close(ch)
			return ch, nil
		},
	})
	fakeNow := time.Unix(0, 0)
	gw.mu.RLock()
	cb := gw.circuitBreakers["recovering-stream"]
	gw.mu.RUnlock()
	if cb == nil {
		t.Fatal("expected circuit breaker for recovering-stream")
	}
	cb.SetNowForTest(func() time.Time { return fakeNow })

	req := streamTestRequest()

	// First stream fails and trips the breaker (FailureThreshold=1 → open).
	if _, err := gw.RouteStream(context.Background(), req); err == nil {
		t.Fatal("expected first stream startup failure to open the breaker")
	}

	if cb.State() != circuitbreaker.StateOpen {
		t.Fatalf("breaker state = %v, want open after failure", cb.State())
	}

	// While still open (timeout not elapsed) the provider is skipped and the
	// open-circuit error surfaces — no probe is consumed.
	if _, err := gw.RouteStream(context.Background(), req); !errors.Is(err, circuitbreaker.ErrCircuitOpen) {
		t.Fatalf("open breaker: error = %v, want ErrCircuitOpen", err)
	}

	// Advance past the open timeout so the breaker is half-open, then the single
	// probe must reach CompleteStream and succeed (regression: resolve-time
	// Allow() would have burned the only permit and rejected the real call).
	fakeNow = fakeNow.Add(5 * time.Millisecond)
	ch, err := gw.RouteStream(context.Background(), req)
	if err != nil {
		t.Fatalf("half-open probe: RouteStream error = %v, want success", err)
	}
	drainMeteredStream(t, ch)
	if got := streamCalls.Load(); got != 2 {
		t.Fatalf("CompleteStream calls = %d, want 2 (initial failure + single half-open probe)", got)
	}
}

func TestShouldRecordCircuitBreakerFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ctx  context.Context
		err  error
		want bool
	}{
		{
			name: "nil error",
			ctx:  context.Background(),
			err:  nil,
			want: false,
		},
		{
			name: "provider timeout with live request context",
			ctx:  context.Background(),
			err:  context.DeadlineExceeded,
			want: true,
		},
		{
			name: "client deadline with canceled request context",
			ctx: func() context.Context {
				ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				cancel()
				return ctx
			}(),
			err:  context.DeadlineExceeded,
			want: false,
		},
		{
			name: "client cancel with canceled request context",
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			}(),
			err:  context.Canceled,
			want: false,
		},
		{
			name: "provider error with live request context",
			ctx:  context.Background(),
			err:  errors.New("upstream unavailable"),
			want: true,
		},
		{
			// The regression: cancellation was inferred from the ERROR's wrapped
			// sentinel rather than from the context. A provider or SDK that reports
			// a client disconnect in its own words — no context.Canceled in the
			// chain — was therefore blamed for it, and a burst of client
			// cancellations opened the breaker on a healthy target.
			name: "client cancel reported by an error that drops the sentinel",
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			}(),
			err:  errors.New("request aborted by peer"),
			want: false,
		},
		{
			// The other side of the same coin, and what keeps the arm above safe:
			// the gateway's OWN deadline is claimed by the context.Cause checks
			// before cancellation is ever considered, so a slow provider still
			// trips the breaker even though ctx.Err() is non-nil here too.
			name: "gateway request timeout still blames the provider",
			ctx: func() context.Context {
				ctx, cancel := context.WithCancelCause(context.Background())
				cancel(ErrRequestTimeout)
				return ctx
			}(),
			err:  errors.New("request aborted by peer"),
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := shouldRecordCircuitBreakerFailure(tt.ctx, tt.err); got != tt.want {
				t.Fatalf("shouldRecordCircuitBreakerFailure() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGateway_Route_ReleasesHalfOpenProbeForIgnoredRateLimit(t *testing.T) {
	var calls atomic.Int32
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets: []config.Target{
			{
				VirtualKey: mockProviderName,
				CircuitBreaker: &config.CircuitBreakerConfig{
					FailureThreshold: 1,
					SuccessThreshold: 1,
					MaxHalfThreshold: 1,
					Timeout:          "1ms",
				},
			},
		},
	})

	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	gw.RegisterProvider(&mockProvider{
		name:   mockProviderName,
		models: []string{"gpt-4o"},
		completeFn: func(context.Context, providers.Request) (*providers.Response, error) {
			switch calls.Add(1) {
			case 1:
				return nil, core.APIError("provider", 500, []byte(`{"error":{"message":"unavailable"}}`))
			case 2:
				return nil, core.APIError("provider", 429, []byte(`{"error":{"message":"rate limited"}}`))
			default:
				return &providers.Response{ID: "recovered", Model: "gpt-4o"}, nil
			}
		},
	})
	fakeNow := time.Unix(0, 0)
	gw.mu.RLock()
	cb := gw.circuitBreakers[mockProviderName]
	gw.mu.RUnlock()
	if cb == nil {
		t.Fatal("expected circuit breaker for mock provider")
	}
	cb.SetNowForTest(func() time.Time { return fakeNow })

	req := providers.Request{Model: "gpt-4o", Messages: []providers.Message{{Role: "user", Content: "hi"}}}
	if _, err := gw.Route(context.Background(), req); err == nil {
		t.Fatal("expected first provider failure to open the circuit")
	}
	fakeNow = fakeNow.Add(5 * time.Millisecond)
	if _, err := gw.Route(context.Background(), req); err == nil || !isRateLimitError(err) {
		t.Fatalf("expected ignored 429 from half-open probe, got %v", err)
	}
	resp, err := gw.Route(context.Background(), req)
	if err != nil {
		t.Fatalf("expected released half-open slot to allow recovery probe, got %v", err)
	}
	if resp.ID != "recovered" {
		t.Fatalf("response ID = %q, want recovered", resp.ID)
	}
}

func TestRecordCircuitBreakerOutcome_ReleasesHalfOpenProbeForClientCancel(t *testing.T) {
	cb := circuitbreaker.New(1, 1, 1, 1*time.Millisecond)
	// Advance a virtual clock instead of sleeping; this test is single-goroutine
	// and the write below happens-before the cb.State() read it gates.
	fakeNow := time.Unix(0, 0)
	cb.SetNowForTest(func() time.Time { return fakeNow })
	cb.RecordFailure()
	fakeNow = fakeNow.Add(5 * time.Millisecond)
	_ = cb.State()
	if !cb.Allow() {
		t.Fatal("expected first half-open stream probe allowed")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	recordCircuitBreakerOutcome(ctx, cb, mockProviderName, context.Canceled)

	if cb.State() != circuitbreaker.StateHalfOpen {
		t.Fatalf("expected ignored client cancel to keep half-open state, got %s", cb.State())
	}
	if !cb.Allow() {
		t.Fatal("expected ignored stream outcome to release half-open probe slot")
	}
}

func TestGateway_RouteStream_FallbackSkipsOpenCircuitBreakerTarget(t *testing.T) {
	var selected atomic.Value
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeFallback},
		Targets: []config.Target{
			{
				VirtualKey: "flaky-stream",
				CircuitBreaker: &config.CircuitBreakerConfig{
					FailureThreshold: 2,
				},
			},
			{VirtualKey: "healthy-stream"},
		},
	})

	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var flakyCalls atomic.Int32
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{
			name:   "flaky-stream",
			models: []string{"gpt-4o"},
		},
		streamFn: func(_ context.Context, _ providers.Request) (<-chan providers.StreamChunk, error) {
			flakyCalls.Add(1)
			return nil, errors.New("stream startup failed")
		},
	})
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{
			name:   "healthy-stream",
			models: []string{"gpt-4o"},
		},
		streamFn: func(_ context.Context, _ providers.Request) (<-chan providers.StreamChunk, error) {
			selected.Store("healthy-stream")
			ch := make(chan providers.StreamChunk, 1)
			ch <- providers.StreamChunk{
				ID: "healthy-ok",
				Choices: []providers.StreamChoice{{
					Delta: providers.MessageDelta{Content: "ok"},
				}},
			}
			close(ch)
			return ch, nil
		},
	})

	req := streamTestRequest()
	for i := 0; i < 2; i++ {
		ch, err := gw.RouteStream(context.Background(), req)
		if err != nil {
			t.Fatalf("attempt %d: RouteStream should fall back after synchronous startup failure: %v", i+1, err)
		}
		drainMeteredStream(t, ch)
	}

	// Snapshot before the final attempt. Now that a synchronous start failure
	// falls back, reaching healthy-stream no longer proves the breaker did
	// anything — an untripped flaky-stream would also fail and fall back to it.
	// The open breaker must skip flaky-stream outright, so its call count has
	// to stay frozen across the third attempt.
	callsBeforeOpen := flakyCalls.Load()

	ch, err := gw.RouteStream(context.Background(), req)
	if err != nil {
		t.Fatalf("RouteStream error = %v, want healthy-stream fallback", err)
	}
	drainMeteredStream(t, ch)
	if got := selected.Load(); got != "healthy-stream" {
		t.Fatalf("selected provider = %v, want healthy-stream", got)
	}
	if got := flakyCalls.Load(); got != callsBeforeOpen {
		t.Fatalf("flaky-stream called %d more time(s) after its breaker opened; the open circuit must skip it, not retry it", got-callsBeforeOpen)
	}
}

// ── Per-request deadline (#277) ───────────────────────────────────────────────

// TestShouldRecordCircuitBreakerFailure_ClientErrorNeverBlamesProvider guards the
// other attribution direction: an unsupported-parameter rejection under
// compatibility.on_unsupported_param=reject is raised by the gateway BEFORE the
// provider is called. Counting it as a provider failure would let one client
// sending a bad parameter take a healthy provider offline for everyone else.
func TestShouldRecordCircuitBreakerFailure_ClientErrorNeverBlamesProvider(t *testing.T) {
	err := &providers.UnsupportedParamError{Provider: "gemini", Params: []string{"logit_bias"}}
	if shouldRecordCircuitBreakerFailure(context.Background(), err) {
		t.Error("a reject-mode unsupported-parameter error is a client error; it must not trip the provider circuit")
	}
}

// TestGateway_WithTargetBreaker_PanicDoesNotStrandHalfOpenProbe pins the panic
// safety of the real withTargetBreaker. Allow() admits one half-open probe; if
// a panic escapes before the outcome is recorded, that permit is never returned
// and resolveState never repairs it — Allow() then rejects this target for the
// life of the process. The bug is invisible on the happy path, so it needs a
// test that panics through the production helper rather than a copy of it.
func TestGateway_WithTargetBreaker_PanicDoesNotStrandHalfOpenProbe(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets: []config.Target{{
			VirtualKey: "panicky",
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

	gw.mu.RLock()
	cb := gw.circuitBreakers["panicky"]
	gw.mu.RUnlock()
	if cb == nil {
		t.Fatal("expected circuit breaker for panicky")
	}
	fakeNow := time.Unix(0, 0)
	cb.SetNowForTest(func() time.Time { return fakeNow })

	ctx := context.Background()
	failing := func(context.Context) error { return errors.New("provider down") }

	// Trip the breaker open (FailureThreshold=1).
	if err := gw.withTargetBreaker(ctx, "panicky", failing); err == nil {
		t.Fatal("expected the failing call to surface its error")
	}
	if cb.State() != circuitbreaker.StateOpen {
		t.Fatalf("breaker state = %v, want open", cb.State())
	}

	// Advance past the timeout so the next call consumes the half-open probe,
	// then panic inside it.
	fakeNow = fakeNow.Add(5 * time.Millisecond)
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Error("panic was swallowed; it must still propagate to the caller")
			}
		}()
		_ = gw.withTargetBreaker(ctx, "panicky", func(context.Context) error {
			panic("provider blew up mid-probe")
		})
	}()

	// The panicking probe must have been resolved as a failure, leaving the
	// breaker open rather than stuck half-open at its probe cap. After another
	// timeout a fresh probe must reach fn.
	fakeNow = fakeNow.Add(5 * time.Millisecond)
	reached := false
	if err := gw.withTargetBreaker(ctx, "panicky", func(context.Context) error {
		reached = true
		return nil
	}); err != nil {
		t.Fatalf("post-panic probe: err = %v, want the call to be admitted", err)
	}
	if !reached {
		t.Fatal("post-panic probe never reached fn: the half-open permit leaked")
	}
}

// blockAfterFirstMock fails on its first Complete call (to trip the circuit
// breaker) and blocks on the second call until release is closed — used to
// hold a half-open probe slot while a concurrent request tests the cap.
type blockAfterFirstMock struct {
	mockProvider
	callN       atomic.Int32
	ready       chan struct{} // closed when the second call enters Complete
	release     chan struct{} // closed to let the second call return
	releaseOnce sync.Once
}

func (m *blockAfterFirstMock) Complete(_ context.Context, _ providers.Request) (*providers.Response, error) {
	if n := m.callN.Add(1); n == 1 {
		return nil, errors.New("provider down")
	}
	close(m.ready)
	<-m.release
	return m.resp, nil
}

// releaseAll unblocks the held second call. It is idempotent so a test can call
// it explicitly and still register it with t.Cleanup as a leak-safety net.
func (m *blockAfterFirstMock) releaseAll() {
	m.releaseOnce.Do(func() { close(m.release) })
}

// TestGateway_Route_EnforcesCircuitBreakerMaxHalfThreshold verifies that the
// MaxHalfThreshold value in CircuitBreakerConfig is wired into the circuit
// breaker: while one half-open probe is in-flight, a concurrent request must
// be rejected with ErrCircuitOpen.
func TestGateway_Route_EnforcesCircuitBreakerMaxHalfThreshold(t *testing.T) {
	mock := &blockAfterFirstMock{
		mockProvider: mockProvider{
			name:   "mock-cb",
			models: []string{"gpt-4o"},
			resp:   &providers.Response{ID: "ok", Model: "gpt-4o"},
		},
		ready:   make(chan struct{}),
		release: make(chan struct{}),
	}

	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets: []config.Target{{
			VirtualKey: "mock-cb",
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

	gw.RegisterProvider(mock)
	// Guarantee the held probe goroutine is released even if an assertion below
	// fails before the explicit release, so it can never leak past the test.
	t.Cleanup(mock.releaseAll)
	fakeNow := time.Unix(0, 0)
	gw.mu.RLock()
	cb := gw.circuitBreakers["mock-cb"]
	gw.mu.RUnlock()
	if cb == nil {
		t.Fatal("expected circuit breaker for mock-cb")
	}
	cb.SetNowForTest(func() time.Time { return fakeNow })

	req := providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	}

	// Trip the circuit: first call fails → circuit opens.
	_, _ = gw.Route(context.Background(), req)

	// Advance past the open timeout.
	fakeNow = fakeNow.Add(5 * time.Millisecond)

	// Probe 1: admitted into half-open, blocks inside provider holding the slot.
	probe1Err := make(chan error, 1)
	go func() {
		_, err := gw.Route(context.Background(), req)
		probe1Err <- err
	}()

	// Wait until probe 1 is holding the in-flight slot (halfOpenProbes=1).
	select {
	case <-mock.ready:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for probe 1 to enter provider")
	}

	// Probe 2: cap=1 already consumed → must be rejected.
	_, err = gw.Route(context.Background(), req)
	if !errors.Is(err, circuitbreaker.ErrCircuitOpen) {
		t.Errorf("expected ErrCircuitOpen for second concurrent half-open probe, got %v", err)
	}

	// Release probe 1 and verify it succeeds.
	mock.releaseAll()
	select {
	case err := <-probe1Err:
		if err != nil {
			t.Errorf("probe 1 expected success, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for probe 1 to complete")
	}
}

// errUpstreamDown is the upstream failure the embedding/image provider below
// returns, standing in for a provider that is genuinely unhealthy.
var errUpstreamDown = errors.New("upstream is down")

// failingMultiModalProvider fails every embedding and image call, so a test can
// drive the target's circuit breaker open through those surfaces alone.
type failingMultiModalProvider struct {
	mockProvider
	calls int
}

func (p *failingMultiModalProvider) Embed(context.Context, providers.EmbeddingRequest) (*providers.EmbeddingResponse, error) {
	p.calls++
	return nil, errUpstreamDown
}

func (p *failingMultiModalProvider) GenerateImage(context.Context, providers.ImageRequest) (*providers.ImageResponse, error) {
	p.calls++
	return nil, errUpstreamDown
}

func newBreakerBoundGateway(t *testing.T, p providers.Provider, failureThreshold int) *Gateway {
	t.Helper()
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets: []config.Target{{
			VirtualKey:     mockProviderName,
			CircuitBreaker: &config.CircuitBreakerConfig{FailureThreshold: failureThreshold},
		}},
	})

	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.RegisterProvider(p)
	return gw
}

// TestGateway_MultiModalSurfaces_FailuresTripBreaker pins that embedding and
// image calls record their outcomes against the target's circuit breaker.
// These surfaces reach the provider through optional interfaces, so they are
// not wrapped by cbProvider; before withTargetBreaker they called the provider
// directly and a dead upstream could never open the circuit through them.
func TestGateway_MultiModalSurfaces_FailuresTripBreaker(t *testing.T) {
	for _, tt := range multiModalCalls {
		t.Run(tt.name, func(t *testing.T) {
			const failureThreshold = 2
			p := &failingMultiModalProvider{
				mockProvider: mockProvider{name: mockProviderName, models: []string{"gpt-4o"}},
			}
			gw := newBreakerBoundGateway(t, p, failureThreshold)

			// Each failure must be blamed on the provider and counted.
			for i := range failureThreshold {
				if err := tt.call(context.Background(), gw); !errors.Is(err, errUpstreamDown) {
					t.Fatalf("call %d = %v, want the upstream error", i, err)
				}
			}

			// The breaker is now open: the next call must be shed WITHOUT reaching
			// the provider at all.
			before := p.calls
			err := tt.call(context.Background(), gw)
			if !errors.Is(err, circuitbreaker.ErrCircuitOpen) {
				t.Fatalf("call after %d failures = %v, want ErrCircuitOpen", failureThreshold, err)
			}
			if p.calls != before {
				t.Errorf("provider was called %d more time(s) with the circuit open; an open circuit must fail fast", p.calls-before)
			}
		})
	}
}

// TestGateway_MultiModalSurfaces_SuccessKeepsBreakerClosed guards the other
// direction: recording outcomes must not trip a breaker on healthy traffic.
func TestGateway_MultiModalSurfaces_SuccessKeepsBreakerClosed(t *testing.T) {
	for _, tt := range multiModalCalls {
		t.Run(tt.name, func(t *testing.T) {
			ep := newBlockingProvider(mockProviderName)
			close(ep.release) // never block: every call succeeds immediately
			gw := newBreakerBoundGateway(t, ep, 1)

			for i := range 3 {
				if err := tt.call(context.Background(), gw); err != nil {
					t.Fatalf("healthy call %d = %v, want nil", i, err)
				}
			}
		})
	}
}

// TestBreakerAndErrorTypeAgreeOnBlame is the invariant the shared classifier
// exists to hold. gateway_provider_errors_total{error_type=provider_error} and
// gateway_circuit_breaker_state are read together during an incident, and they
// were answering differently about the same error: the breaker exonerated a
// concurrency shed and a client disconnect while the unary metric convicted the
// provider for both. Anything the breaker refuses to blame must therefore carry
// an error_type that is not provider_error, and vice versa.
func TestBreakerAndErrorTypeAgreeOnBlame(t *testing.T) {
	callerGone, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name       string
		ctx        context.Context
		err        error
		wantBlamed bool
	}{
		{"concurrency shed", context.Background(), core.ErrProviderSaturated, false},
		{"caller hung up", callerGone, errors.New("request aborted"), false},
		{"upstream 500", context.Background(), errors.New("500 internal server error"), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blamed := shouldRecordCircuitBreakerFailure(tt.ctx, tt.err)
			if blamed != tt.wantBlamed {
				t.Fatalf("shouldRecordCircuitBreakerFailure = %v, want %v", blamed, tt.wantBlamed)
			}
			isProviderError := metrics.ProviderErrorType(tt.ctx, tt.err) == metrics.ErrTypeProvider
			if isProviderError != tt.wantBlamed {
				t.Errorf("error_type=provider_error = %v but the breaker blames the provider = %v; the two signals contradict each other",
					isProviderError, tt.wantBlamed)
			}
		})
	}
}

// TestGateway_RouteStream_ReloadKeepsOutcomeOnAdmittingBreaker guards the
// identity half of the streaming breaker contract: the outcome of a stream must
// resolve on the breaker INSTANCE that admitted its start, not on whatever
// instance sits under the target's key when the stream ends. A ReloadConfig
// that changes the target's circuit_breaker block retires the old instance and
// installs a fresh one under the same key — a by-name lookup at stream end then
// scores the outcome against a breaker whose state machine never admitted the
// call.
func TestGateway_RouteStream_ReloadKeepsOutcomeOnAdmittingBreaker(t *testing.T) {
	cfgFor := func(successThreshold int) config.Config {
		return config.Config{
			Strategy: config.StrategyConfig{Mode: config.ModeSingle},
			Targets: []config.Target{{
				VirtualKey: "reload-stream",
				CircuitBreaker: &config.CircuitBreakerConfig{
					FailureThreshold: 1,
					SuccessThreshold: successThreshold,
				},
			}},
		}
	}
	gw, err := newTestGateway(t, cfgFor(1))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// The provider blocks inside CompleteStream until released, holding the
	// stream start in flight — the window in which a reload can retire the
	// breaker that has already admitted the probe.
	raw := make(chan providers.StreamChunk)
	started := make(chan struct{})
	release := make(chan struct{})
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: "reload-stream", models: []string{"gpt-4o"}},
		streamFn: func(context.Context, providers.Request) (<-chan providers.StreamChunk, error) {
			close(started)
			<-release
			return raw, nil
		},
	})

	type startResult struct {
		ch  <-chan providers.StreamChunk
		err error
	}
	startDone := make(chan startResult, 1)
	go func() {
		ch, err := gw.RouteStream(context.Background(), streamTestRequest())
		startDone <- startResult{ch, err}
	}()

	// The start is now in flight on the original breaker. Capture that
	// instance, then reload with a changed block so the key resolves to a
	// fresh instance before the start returns.
	<-started
	gw.mu.RLock()
	admittedCB := gw.circuitBreakers["reload-stream"]
	gw.mu.RUnlock()
	if admittedCB == nil {
		t.Fatal("no breaker was built for the streaming target")
	}
	if err := gw.ReloadConfig(t.Context(), cfgFor(2)); err != nil {
		t.Fatalf("ReloadConfig: %v", err)
	}
	close(release)

	res := <-startDone
	if res.err != nil {
		t.Fatalf("RouteStream: %v", res.err)
	}
	ch := res.ch

	// Fail the stream mid-flight and drain to completion. The metering
	// goroutine closes the wrapped channel only after resolving the breaker
	// outcome, so the drain returning means the outcome has landed.
	raw <- providers.StreamChunk{Error: errors.New("upstream exploded mid-stream")}
	close(raw)
	drainMeteredStream(t, ch)

	if got := admittedCB.State(); got != circuitbreaker.StateOpen {
		t.Errorf("admitting breaker state = %v, want Open: the failure belongs to the instance that admitted the stream", got)
	}
	gw.mu.RLock()
	freshCB := gw.circuitBreakers["reload-stream"]
	gw.mu.RUnlock()
	if freshCB == nil {
		t.Fatal("reload did not rebuild the breaker for the changed block")
	}
	if freshCB == admittedCB {
		t.Fatal("reload kept the old breaker instance; the changed block should have retired it")
	}
	if got := freshCB.State(); got != circuitbreaker.StateClosed {
		t.Errorf("fresh breaker state = %v, want Closed: it admitted nothing and must not inherit the old stream's failure", got)
	}
}
