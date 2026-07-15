package aigateway

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/circuitbreaker"
	"github.com/ferro-labs/ai-gateway/providers"
)

type blockAfterFirstMock struct {
	mockProvider
	callN   atomic.Int32
	ready   chan struct{} // closed when the second call enters Complete
	release chan struct{} // closed to let the second call return
}

func (m *blockAfterFirstMock) Complete(_ context.Context, _ providers.Request) (*providers.Response, error) {
	if n := m.callN.Add(1); n == 1 {
		return nil, errors.New("provider down")
	}
	close(m.ready)
	<-m.release
	return m.resp, nil
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

	gw, err := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets: []Target{{
			VirtualKey: "mock-cb",
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

	gw.RegisterProvider(mock)
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
	close(mock.release)
	select {
	case err := <-probe1Err:
		if err != nil {
			t.Errorf("probe 1 expected success, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for probe 1 to complete")
	}
}
