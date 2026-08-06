package aigateway

import (
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/pkg/metrics"
	"github.com/prometheus/client_golang/prometheus"
)

// breakerGaugeSeries reads the gateway_circuit_breaker_state sample for one
// provider out of the default registry, and reports whether a series exists at
// all. Presence has to be distinguishable from a zero value: "no breaker
// configured" and "breaker closed" are different answers.
func breakerGaugeSeries(t *testing.T, provider string) (float64, bool) {
	t.Helper()

	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range families {
		if mf.GetName() != "gateway_circuit_breaker_state" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, label := range m.GetLabel() {
				if label.GetName() == "provider" && label.GetValue() == provider {
					return m.GetGauge().GetValue(), true
				}
			}
		}
	}
	return 0, false
}

// gatewayWithBreaker builds a gateway whose single target has a breaker that
// trips after one failure and reopens after timeout, and publishes it as the
// process metric source for the duration of the test.
func gatewayWithBreaker(t *testing.T, provider string, timeout string) *Gateway {
	t.Helper()

	gw, err := New(config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets: []config.Target{{
			VirtualKey: provider,
			CircuitBreaker: &config.CircuitBreakerConfig{
				FailureThreshold: 1,
				SuccessThreshold: 1,
				Timeout:          timeout,
			},
		}},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	t.Cleanup(func() { _ = gw.Close() })

	gw.PublishCircuitBreakerMetrics()
	t.Cleanup(func() { metrics.SetCircuitBreakerStateSource(nil) })
	return gw
}

// TestBreakerGaugeClearsWithoutTraffic is the regression test for a gauge that
// reported "open" forever.
//
// The gauge used to be written only from a request outcome, but a breaker
// leaves Open on a timer that no request observes. With traffic stopped — which
// is exactly what happens once a provider is failing and callers back off —
// nothing was left to write it, so an alert on circuit_breaker_state == 1 could
// never clear itself. Not one request is sent after the breaker trips here.
func TestBreakerGaugeClearsWithoutTraffic(t *testing.T) {
	const provider = "gauge-decay"
	gw := gatewayWithBreaker(t, provider, "20ms")

	cb := gw.circuitBreakers[provider]
	if cb == nil {
		t.Fatal("precondition: no breaker was created for the configured target")
	}

	cb.RecordFailure() // failure_threshold is 1, so this opens it
	if v, ok := breakerGaugeSeries(t, provider); !ok || v != metrics.CircuitOpen {
		t.Fatalf("gauge = %v (present=%v) just after the breaker tripped, want %v", v, ok, metrics.CircuitOpen)
	}

	// Past the open timeout, with no traffic whatsoever.
	time.Sleep(60 * time.Millisecond)

	v, ok := breakerGaugeSeries(t, provider)
	if !ok {
		t.Fatal("the series disappeared while the breaker was still configured")
	}
	if v != metrics.CircuitHalfOpen {
		t.Errorf("gauge = %v after the open timeout elapsed with no traffic, want %v (half_open); "+
			"a gauge that only a request can update reports a recovered breaker as open forever", v, metrics.CircuitHalfOpen)
	}
}

// TestBreakerGaugePresentBeforeAnyRequest covers the companion complaint: the
// series used to be created on first use, so an absent series meant either "no
// breaker configured" or "configured but never exercised" and an operator could
// not tell which.
func TestBreakerGaugePresentBeforeAnyRequest(t *testing.T) {
	const provider = "gauge-cold"
	gatewayWithBreaker(t, provider, "1s")

	v, ok := breakerGaugeSeries(t, provider)
	if !ok {
		t.Fatal("no series for a configured breaker that has not served a request yet; " +
			"absent must mean unconfigured, or it means nothing")
	}
	if v != metrics.CircuitClosed {
		t.Errorf("gauge = %v for a fresh breaker, want %v (closed)", v, metrics.CircuitClosed)
	}
}

// TestBreakerGaugeAbsentWithoutABreaker is the other half of the same contract.
func TestBreakerGaugeAbsentWithoutABreaker(t *testing.T) {
	const provider = "gauge-unconfigured"

	gw, err := New(config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: provider}}, // no circuit_breaker block
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	t.Cleanup(func() { _ = gw.Close() })

	gw.PublishCircuitBreakerMetrics()
	t.Cleanup(func() { metrics.SetCircuitBreakerStateSource(nil) })

	if v, ok := breakerGaugeSeries(t, provider); ok {
		t.Errorf("a target with no breaker published a series at %v; absent is what says 'not configured'", v)
	}
}

// TestBreakerGaugeSourceCanBeRemoved keeps SetCircuitBreakerStateSource(nil)
// honest, so a retired gateway stops being reported rather than being read
// after Close.
func TestBreakerGaugeSourceCanBeRemoved(t *testing.T) {
	const provider = "gauge-retired"
	gatewayWithBreaker(t, provider, "1s")

	if _, ok := breakerGaugeSeries(t, provider); !ok {
		t.Fatal("precondition: no series published for a configured breaker")
	}

	metrics.SetCircuitBreakerStateSource(nil)
	if _, ok := breakerGaugeSeries(t, provider); ok {
		t.Error("series still published after the source was removed")
	}
}
