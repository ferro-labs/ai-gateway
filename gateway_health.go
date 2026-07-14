package aigateway

import "github.com/ferro-labs/ai-gateway/internal/circuitbreaker"

// Circuit-state strings reported by Readiness.
const (
	circuitClosed   = "closed"
	circuitOpen     = "open"
	circuitHalfOpen = "half-open"
)

// Readiness is a point-in-time snapshot of whether the gateway can serve
// traffic, together with each registered provider's circuit-breaker state.
type Readiness struct {
	// Ready reports whether at least one registered provider has a non-open
	// circuit (closed or half-open). It is false when no providers are
	// registered.
	Ready bool
	// Providers holds one entry per registered provider, in registration order.
	Providers []ProviderReadiness
}

// ProviderReadiness reports a single provider's availability.
type ProviderReadiness struct {
	// Name is the provider's registered name.
	Name string
	// Circuit is the provider's circuit-breaker state: "closed", "open", or
	// "half-open". A provider without a configured circuit breaker reports
	// "closed".
	Circuit string
}

// Readiness returns a snapshot of provider availability derived from each
// provider's circuit-breaker state. Reading the state is non-consuming: it does
// not admit or spend a half-open probe. A provider without a configured circuit
// breaker counts as available ("closed"). The gateway is ready when at least one
// provider has a non-open circuit.
func (g *Gateway) Readiness() Readiness {
	g.mu.RLock()
	defer g.mu.RUnlock()

	provs := make([]ProviderReadiness, 0, len(g.providerNames))
	ready := false
	for _, name := range g.providerNames {
		circuit := circuitClosed
		if cb, ok := g.circuitBreakers[name]; ok {
			circuit = circuitStateString(cb.State())
		}
		if circuit != circuitOpen {
			ready = true
		}
		provs = append(provs, ProviderReadiness{Name: name, Circuit: circuit})
	}
	return Readiness{Ready: ready, Providers: provs}
}

// circuitStateString maps a circuit-breaker state to the readiness vocabulary
// ("closed"/"open"/"half-open").
func circuitStateString(s circuitbreaker.State) string {
	switch s {
	case circuitbreaker.StateOpen:
		return circuitOpen
	case circuitbreaker.StateHalfOpen:
		return circuitHalfOpen
	default:
		return circuitClosed
	}
}
