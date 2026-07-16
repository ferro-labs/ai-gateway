package aigateway

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/circuitbreaker"
	"github.com/ferro-labs/ai-gateway/providers"
)

// TestGateway_Route_RequestTimeoutTripsCircuitBreaker guards the attribution of a
// gateway-imposed deadline. A provider that hangs past request_timeout is a
// PROVIDER failure and must trip its breaker. If it is misread as caller
// cancellation the breaker never opens, the hung provider stays in rotation
// forever, and /readyz — whose only provider signal is circuit state — keeps
// reporting the pod ready while every request fails.
func TestGateway_Route_RequestTimeoutTripsCircuitBreaker(t *testing.T) {
	gw, err := newTestGateway(t, Config{
		Strategy:       StrategyConfig{Mode: ModeSingle},
		RequestTimeout: "30ms",
		Targets: []Target{{
			VirtualKey:     "hung",
			CircuitBreaker: &CircuitBreakerConfig{FailureThreshold: 2, Timeout: "1m"},
		}},
	})

	if err != nil {
		t.Fatalf("New: %v", err)
	}

	gw.RegisterProvider(&slowCompleteProvider{
		mockProvider: mockProvider{name: "hung", models: []string{"gpt-4o"}},
	})

	for range 3 {
		if _, err := gw.Route(context.Background(), providers.Request{Model: "gpt-4o"}); err == nil {
			t.Fatal("expected the hung provider to exceed request_timeout")
		}
	}

	circuit := ""
	for _, p := range gw.Readiness().Providers {
		if p.Name == "hung" {
			circuit = p.Circuit
		}
	}
	if circuit != "open" {
		t.Errorf("circuit = %q, want \"open\": a provider hanging past the gateway's own request_timeout "+
			"is a provider failure and must trip the breaker", circuit)
	}
}

func TestGateway_Route_RequestTimeoutBoundsTheRequest(t *testing.T) {
	gw, err := newTestGateway(t, Config{
		Strategy:       StrategyConfig{Mode: ModeSingle},
		Targets:        []Target{{VirtualKey: "slow"}},
		RequestTimeout: "50ms",
	})

	if err != nil {
		t.Fatalf("New: %v", err)
	}

	gw.RegisterProvider(&slowCompleteProvider{
		mockProvider: mockProvider{name: "slow", models: []string{"gpt-4o"}},
	})

	start := time.Now()
	_, err = gw.Route(context.Background(), providers.Request{Model: "gpt-4o"})
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Route error = %v, want context.DeadlineExceeded", err)
	}
	// The provider would take 500ms; the 50ms request_timeout must cut it short.
	if elapsed >= 500*time.Millisecond {
		t.Errorf("request took %v — the 50ms request_timeout did not bound it", elapsed)
	}
}

func TestGateway_Route_NoRequestTimeout_LeavesRequestUnbounded(t *testing.T) {
	// With request_timeout omitted the gateway imposes no deadline of its own, so
	// the slow provider runs to completion.
	gw, err := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "slow"}},
	})

	if err != nil {
		t.Fatalf("New: %v", err)
	}

	gw.RegisterProvider(&slowCompleteProvider{
		mockProvider: mockProvider{name: "slow", models: []string{"gpt-4o"}},
	})

	resp, err := gw.Route(context.Background(), providers.Request{Model: "gpt-4o"})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if resp.ID != "ok" {
		t.Errorf("response ID = %q, want %q", resp.ID, "ok")
	}
}

type slowCompleteProvider struct {
	mockProvider
}

func (p *slowCompleteProvider) Complete(ctx context.Context, _ providers.Request) (*providers.Response, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(500 * time.Millisecond):
		return &providers.Response{ID: "ok"}, nil
	}
}

func TestGateway_Route_ClientDeadlineDoesNotTripCircuit(t *testing.T) {
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
	gw.RegisterProvider(&slowCompleteProvider{
		mockProvider: mockProvider{
			name:   "slow",
			models: []string{"gpt-4o"},
		},
	})

	req := providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	}
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		_, routeErr := gw.Route(ctx, req)
		cancel()
		if !errors.Is(routeErr, context.DeadlineExceeded) {
			t.Fatalf("attempt %d: error = %v, want context.DeadlineExceeded", i+1, routeErr)
		}
	}

	gw.mu.RLock()
	cb := gw.circuitBreakers["slow"]
	gw.mu.RUnlock()
	if cb == nil {
		t.Fatal("expected circuit breaker for slow")
	}
	if cb.State() != circuitbreaker.StateClosed {
		t.Fatalf("circuit state = %v, want closed after client deadlines", cb.State())
	}
}

func TestGateway_RouteStream_ClientDeadlineDoesNotTripCircuit(t *testing.T) {
	gw, err := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets: []Target{{
			VirtualKey: "slow-stream",
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
			name:   "slow-stream",
			models: []string{"gpt-4o"},
		},
		streamFn: func(ctx context.Context, _ providers.Request) (<-chan providers.StreamChunk, error) {
			ch := make(chan providers.StreamChunk)
			go func() {
				defer close(ch)
				ticker := time.NewTicker(20 * time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						select {
						case ch <- providers.StreamChunk{
							Choices: []providers.StreamChoice{{
								Delta: providers.MessageDelta{Content: "x"},
							}},
						}:
						case <-ctx.Done():
							return
						}
					}
				}
			}()
			return ch, nil
		},
	})

	req := streamTestRequest()
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		ch, streamErr := gw.RouteStream(ctx, req)
		if streamErr != nil {
			cancel()
			t.Fatalf("attempt %d: RouteStream error = %v", i+1, streamErr)
		}
		for range ch { //nolint:revive // empty-block: intentionally draining the stream to completion
		}
		cancel()
	}

	gw.mu.RLock()
	cb := gw.circuitBreakers["slow-stream"]
	gw.mu.RUnlock()
	if cb == nil {
		t.Fatal("expected circuit breaker for slow-stream")
	}
	if cb.State() != circuitbreaker.StateClosed {
		t.Fatalf("circuit state = %v, want closed after client deadlines", cb.State())
	}
}

func TestGateway_RouteStream_MidStreamFailureTripsCircuit(t *testing.T) {
	streamErr := errors.New("mid-stream provider failure")
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
		streamFn: func(_ context.Context, _ providers.Request) (<-chan providers.StreamChunk, error) {
			ch := make(chan providers.StreamChunk, 2)
			ch <- providers.StreamChunk{
				Choices: []providers.StreamChoice{{
					Delta: providers.MessageDelta{Content: "partial"},
				}},
			}
			ch <- providers.StreamChunk{Error: streamErr}
			close(ch)
			return ch, nil
		},
	})

	req := streamTestRequest()
	for i := 0; i < 2; i++ {
		ch, err := gw.RouteStream(context.Background(), req)
		if err != nil {
			t.Fatalf("attempt %d: RouteStream error = %v", i+1, err)
		}
		drainMeteredStream(t, ch)
	}

	_, err = gw.RouteStream(context.Background(), req)
	if !errors.Is(err, circuitbreaker.ErrCircuitOpen) {
		t.Fatalf("expected circuit open after mid-stream failures, got %v", err)
	}
}
