package aigateway

import (
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/config"
)

// TestReadinessIsDerivedFromTargetsNotRegistrations is the regression test for a
// config that deploys entirely green while failing every request: a target names
// a provider that is not registered, so routing has nothing to dispatch to, but
// readiness used to count *registered providers* instead of *routable targets*.
// Nine healthy providers plus one unroutable target reported "ready" while 100%
// of traffic returned 500 "provider not found".
func TestReadinessIsDerivedFromTargetsNotRegistrations(t *testing.T) {
	tests := []struct {
		name string
		// targets configured; registered lists the providers actually present.
		targets      []string
		registered   []string
		wantReady    bool
		wantRoutable map[string]bool
	}{
		{
			name:         "every target resolves",
			targets:      []string{"a", "b"},
			registered:   []string{"a", "b"},
			wantReady:    true,
			wantRoutable: map[string]bool{"a": true, "b": true},
		},
		{
			name:       "sole target resolves to nothing while other providers are healthy",
			targets:    []string{"typo-provider"},
			registered: []string{"a", "b", "c"},
			// The providers are fine; none of them is reachable through this
			// config, so the instance cannot serve a single request.
			wantReady:    false,
			wantRoutable: map[string]bool{"typo-provider": false},
		},
		{
			name:       "one of two targets resolves is degraded, not dead",
			targets:    []string{"a", "typo-provider"},
			registered: []string{"a"},
			// Fallback skips the unroutable target; requests still succeed.
			wantReady:    true,
			wantRoutable: map[string]bool{"a": true, "typo-provider": false},
		},
		{
			name:         "no providers registered at all",
			targets:      []string{"a"},
			registered:   nil,
			wantReady:    false,
			wantRoutable: map[string]bool{"a": false},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			targets := make([]config.Target, 0, len(tt.targets))
			for _, name := range tt.targets {
				targets = append(targets, config.Target{VirtualKey: name})
			}
			gw, err := newTestGateway(t, config.Config{
				Strategy: config.StrategyConfig{Mode: config.ModeFallback},
				Targets:  targets,
			})
			if err != nil {
				t.Fatalf("New gateway: %v", err)
			}
			for _, name := range tt.registered {
				gw.RegisterProvider(&mockProvider{name: name})
			}

			got := gw.Readiness()
			if got.Ready != tt.wantReady {
				t.Errorf("Ready = %v, want %v (targets %v, registered %v)",
					got.Ready, tt.wantReady, tt.targets, tt.registered)
			}
			if len(got.Targets) != len(tt.wantRoutable) {
				t.Fatalf("got %d targets, want %d: %+v", len(got.Targets), len(tt.wantRoutable), got.Targets)
			}
			for _, tr := range got.Targets {
				want, ok := tt.wantRoutable[tr.Name]
				if !ok {
					t.Errorf("unexpected target %q", tr.Name)
					continue
				}
				if tr.Routable != want {
					t.Errorf("target %q Routable = %v, want %v", tr.Name, tr.Routable, want)
				}
			}
		})
	}
}

// TestReadinessTargetWithOpenCircuitIsNotRoutable keeps the pre-existing
// circuit-breaker contract intact now that Ready is computed over targets: a
// target whose provider is registered but whose circuit is open cannot carry
// traffic, so it must not hold the instance in rotation on its own.
func TestReadinessTargetWithOpenCircuitIsNotRoutable(t *testing.T) {
	cbCfg := &config.CircuitBreakerConfig{
		FailureThreshold: 1,
		SuccessThreshold: 1,
		MaxHalfThreshold: 1,
		Timeout:          "1m",
	}
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeFallback},
		Targets:  []config.Target{{VirtualKey: "a", CircuitBreaker: cbCfg}},
	})
	if err != nil {
		t.Fatalf("New gateway: %v", err)
	}
	gw.RegisterProvider(&mockProvider{name: "a"})

	cb := gw.circuitBreakers["a"]
	base := time.Now()
	cb.SetNowForTest(func() time.Time { return base })
	cb.RecordFailure()

	got := gw.Readiness()
	if got.Ready {
		t.Error("Ready = true, want false when the only target's circuit is open")
	}
	if len(got.Targets) != 1 || got.Targets[0].Routable {
		t.Errorf("targets = %+v, want the single target reported unroutable", got.Targets)
	}
}
