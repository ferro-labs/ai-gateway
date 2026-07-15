package aigateway

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/circuitbreaker"
	"github.com/ferro-labs/ai-gateway/internal/metrics"
	"github.com/ferro-labs/ai-gateway/providers"
)

func TestGateway_RouteStream_ImmediateFailure_IncrementsProviderErrors(t *testing.T) {
	gw, _ := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "mock-stream"}},
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
	gw, _ := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "mock-stream"}},
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
	gw, err := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets: []Target{{
			VirtualKey: "flaky-stream",
			CircuitBreaker: &CircuitBreakerConfig{
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
	gw, err := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeFallback},
		Targets: []Target{
			{
				VirtualKey: "plain",
				CircuitBreaker: &CircuitBreakerConfig{
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
	gw, err := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets: []Target{{
			VirtualKey: "flaky-stream",
			CircuitBreaker: &CircuitBreakerConfig{
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
	gw, err := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets: []Target{{
			VirtualKey: "slow",
			CircuitBreaker: &CircuitBreakerConfig{
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
	gw, err := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets: []Target{{
			VirtualKey: "flaky-stream",
			CircuitBreaker: &CircuitBreakerConfig{
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
	gw, err := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets: []Target{{
			VirtualKey: "recovering-stream",
			CircuitBreaker: &CircuitBreakerConfig{
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
	gw, err := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets: []Target{
			{
				VirtualKey: mockProviderName,
				CircuitBreaker: &CircuitBreakerConfig{
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
				return nil, errors.New("provider API error (500): unavailable")
			case 2:
				return nil, errors.New("provider API error (429): rate limited")
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
	gw, err := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeFallback},
		Targets: []Target{
			{
				VirtualKey: "flaky-stream",
				CircuitBreaker: &CircuitBreakerConfig{
					FailureThreshold: 2,
				},
			},
			{VirtualKey: "healthy-stream"},
		},
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
		_, err := gw.RouteStream(context.Background(), req)
		if err == nil {
			t.Fatalf("attempt %d: expected startup error to trip breaker", i+1)
		}
	}

	ch, err := gw.RouteStream(context.Background(), req)
	if err != nil {
		t.Fatalf("RouteStream error = %v, want healthy-stream fallback", err)
	}
	drainMeteredStream(t, ch)
	if got := selected.Load(); got != "healthy-stream" {
		t.Fatalf("selected provider = %v, want healthy-stream", got)
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

// TestGateway_Route_RequestTimeoutTripsCircuitBreaker guards the attribution of a
// gateway-imposed deadline. A provider that hangs past request_timeout is a
// PROVIDER failure and must trip its breaker. If it is misread as caller
// cancellation the breaker never opens, the hung provider stays in rotation
// forever, and /readyz — whose only provider signal is circuit state — keeps
// reporting the pod ready while every request fails.
