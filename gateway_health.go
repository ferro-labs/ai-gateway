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
	// MCPServers holds one entry per registered MCP server, in registration
	// order. Empty when no MCP servers are configured.
	//
	// It is reported separately from Ready on purpose: an MCP server being down
	// says nothing about whether providers can serve, and folding the two into
	// one boolean would leave a caller unable to tell which failed.
	MCPServers []MCPServerReadiness
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

// MCPServerReadiness reports a single MCP server's availability.
type MCPServerReadiness struct {
	// Name is the server's configured name.
	Name string
	// Ready reports whether the server completed initialization and its
	// transport is still live.
	Ready bool
	// Required reports whether this server's availability gates gateway
	// readiness, mirroring its `required` config field.
	Required bool
	// LastError is the most recent initialization or transport failure, empty
	// when the server is ready. It can quote a URL, host, or command line, so
	// callers must treat it as sensitive: log it, but never serve it on an
	// unauthenticated endpoint.
	LastError string
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
	return Readiness{Ready: ready, Providers: provs, MCPServers: g.mcpReadiness()}
}

// mcpReadiness projects the MCP registry's status snapshot onto the readiness
// shape. Caller holds g.mu. Returns nil when no MCP servers are configured.
func (g *Gateway) mcpReadiness() []MCPServerReadiness {
	if g.mcpRegistry == nil {
		return nil
	}
	status := g.mcpRegistry.Status()
	if len(status) == 0 {
		return nil
	}
	out := make([]MCPServerReadiness, 0, len(status))
	for _, s := range status {
		out = append(out, MCPServerReadiness{
			Name:      s.Name,
			Ready:     s.Ready,
			Required:  s.Required,
			LastError: s.LastError,
		})
	}
	return out
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
