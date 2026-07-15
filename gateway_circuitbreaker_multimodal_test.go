package aigateway

import (
	"context"
	"errors"
	"testing"

	"github.com/ferro-labs/ai-gateway/internal/circuitbreaker"
	"github.com/ferro-labs/ai-gateway/providers"
)

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
	gw, err := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets: []Target{{
			VirtualKey:     mockProviderName,
			CircuitBreaker: &CircuitBreakerConfig{FailureThreshold: failureThreshold},
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
