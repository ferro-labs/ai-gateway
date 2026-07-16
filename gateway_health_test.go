package aigateway

import (
	"testing"
	"time"
)

func TestGatewayReadiness(t *testing.T) {
	const cbTimeout = time.Minute
	cbCfg := func() *CircuitBreakerConfig {
		return &CircuitBreakerConfig{
			FailureThreshold: 1,
			SuccessThreshold: 1,
			MaxHalfThreshold: 1,
			Timeout:          cbTimeout.String(),
		}
	}

	// tripOpen drives a breaker to Open and freezes its clock so State stays Open.
	tripOpen := func(g *Gateway, name string) {
		cb := g.circuitBreakers[name]
		base := time.Now()
		cb.SetNowForTest(func() time.Time { return base })
		cb.RecordFailure()
	}
	// tripHalfOpen opens a breaker then advances its clock past the open timeout
	// so the next State read resolves to HalfOpen.
	tripHalfOpen := func(g *Gateway, name string) {
		cb := g.circuitBreakers[name]
		base := time.Now()
		cb.SetNowForTest(func() time.Time { return base })
		cb.RecordFailure()
		cb.SetNowForTest(func() time.Time { return base.Add(cbTimeout + time.Second) })
	}

	tests := []struct {
		name        string
		setup       func(t *testing.T) *Gateway
		wantReady   bool
		wantCircuit map[string]string
	}{
		{
			name: "mixed circuits with a no-breaker provider",
			setup: func(t *testing.T) *Gateway {
				gw := newReadinessGateway(t, []Target{
					{VirtualKey: "closed-prov", CircuitBreaker: cbCfg()},
					{VirtualKey: "open-prov", CircuitBreaker: cbCfg()},
					{VirtualKey: "half-prov", CircuitBreaker: cbCfg()},
					{VirtualKey: "nocb-prov"},
				})
				tripOpen(gw, "open-prov")
				tripHalfOpen(gw, "half-prov")
				return gw
			},
			wantReady: true,
			wantCircuit: map[string]string{
				"closed-prov": "closed",
				"open-prov":   "open",
				"half-prov":   "half-open",
				"nocb-prov":   "closed",
			},
		},
		{
			name: "all providers open is not ready",
			setup: func(t *testing.T) *Gateway {
				gw := newReadinessGateway(t, []Target{
					{VirtualKey: "a", CircuitBreaker: cbCfg()},
					{VirtualKey: "b", CircuitBreaker: cbCfg()},
				})
				tripOpen(gw, "a")
				tripOpen(gw, "b")
				return gw
			},
			wantReady:   false,
			wantCircuit: map[string]string{"a": "open", "b": "open"},
		},
		{
			name: "half-open counts as ready",
			setup: func(t *testing.T) *Gateway {
				gw := newReadinessGateway(t, []Target{
					{VirtualKey: "a", CircuitBreaker: cbCfg()},
				})
				tripHalfOpen(gw, "a")
				return gw
			},
			wantReady:   true,
			wantCircuit: map[string]string{"a": "half-open"},
		},
		{
			name: "zero registered providers is not ready",
			setup: func(t *testing.T) *Gateway {
				// A configured target with no provider registered: the gateway
				// requires at least one target, but readiness counts registered
				// providers, so this reports zero.
				gw, err := newTestGateway(t, Config{
					Strategy: StrategyConfig{Mode: ModeFallback},
					Targets:  []Target{{VirtualKey: "unregistered"}},
				})

				if err != nil {
					t.Fatalf("New gateway: %v", err)
				}
				return gw
			},
			wantReady:   false,
			wantCircuit: map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gw := tt.setup(t)
			got := gw.Readiness()

			if got.Ready != tt.wantReady {
				t.Errorf("Ready = %v, want %v", got.Ready, tt.wantReady)
			}
			if len(got.Providers) != len(tt.wantCircuit) {
				t.Fatalf("got %d providers, want %d: %+v", len(got.Providers), len(tt.wantCircuit), got.Providers)
			}
			for _, p := range got.Providers {
				want, ok := tt.wantCircuit[p.Name]
				if !ok {
					t.Errorf("unexpected provider %q", p.Name)
					continue
				}
				if p.Circuit != want {
					t.Errorf("provider %q circuit = %q, want %q", p.Name, p.Circuit, want)
				}
			}
		})
	}
}

// newReadinessGateway builds a gateway with the given targets and registers a
// stub provider for every target so ListProviders and the circuit-breaker map
// are populated.
func newReadinessGateway(t *testing.T, targets []Target) *Gateway {
	t.Helper()
	gw, err := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeFallback},
		Targets:  targets,
	})

	if err != nil {
		t.Fatalf("New gateway: %v", err)
	}
	for _, target := range targets {
		gw.RegisterProvider(&mockProvider{name: target.VirtualKey})
	}
	return gw
}
