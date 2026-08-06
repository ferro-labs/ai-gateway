package aigateway

import (
	"github.com/ferro-labs/ai-gateway/pkg/circuitbreaker"
	"github.com/ferro-labs/ai-gateway/pkg/metrics"
)

// Circuit-state strings reported by Readiness.
const (
	circuitClosed   = "closed"
	circuitOpen     = "open"
	circuitHalfOpen = "half-open"
)

// Readiness is a point-in-time snapshot of whether the gateway can serve
// traffic, together with each registered provider's circuit-breaker state.
type Readiness struct {
	// Ready reports whether at least one configured target is routable: a
	// provider is registered under that target's name and its circuit is not
	// open.
	//
	// It is measured over *targets*, not over registered providers, because the
	// two sets can be disjoint. Registration is driven by which credentials the
	// environment holds; routing is driven by which names the config lists. A
	// config whose targets name no registered provider fails 100% of traffic
	// while every provider in the process is perfectly healthy, and counting
	// registrations reports that instance ready.
	//
	// The threshold is deliberately "at least one", not "all". One unroutable
	// target among several is degraded, not dead — fallback and load-balance
	// skip it and the rest keep serving — and pulling an instance that still
	// answers most requests out of rotation makes the outage worse rather than
	// better. Zero routable targets is categorically different: nothing the
	// router can select exists, so every request fails and the instance must
	// leave rotation.
	//
	// Known ceiling: a target counts as routable whether or not the configured
	// strategy would ever select it. Under `mode: single` only the first target
	// is ever used, so a config with a broken first target and a healthy second
	// still reports ready. Ask the strategy which targets it would select if
	// that case starts to bite.
	Ready bool
	// Providers holds one entry per registered provider, in registration order.
	Providers []ProviderReadiness
	// Targets holds one entry per configured target, in config order. Ready is
	// derived from it, and it is reported in full so an operator can see *which*
	// target is unroutable rather than only that one is.
	Targets []TargetReadiness
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
	// "half-open".
	//
	// A provider with no circuit breaker configured also reports "closed", and
	// so does a provider that is registered but named by no target. That is
	// deliberate rather than an omission: this field answers "would a call be
	// admitted", and for a provider with no breaker the answer is yes — the
	// same yes a closed breaker gives. Widening the vocabulary would change a
	// value every existing health consumer already parses in order to express a
	// distinction that does not affect whether traffic flows.
	//
	// The distinction that does matter to an operator — is a breaker configured
	// here at all — is answered by gateway_circuit_breaker_state instead, which
	// publishes a series for exactly the providers that have one (see
	// Gateway.CircuitBreakerStates). Absent there means unconfigured.
	Circuit string
}

// TargetReadiness reports whether one configured target can carry traffic.
type TargetReadiness struct {
	// Name is the target's virtual_key as written in the config.
	Name string
	// Routable reports whether a provider is registered under Name and its
	// circuit is not open. False covers both "no provider was ever registered
	// under this name" — a typo in virtual_key, or a credential this deployment
	// does not hold — and "registered, but its circuit is open".
	Routable bool
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
// provider's circuit-breaker state, plus whether each configured target resolves
// to one of those providers. Reading circuit state is non-consuming: it does not
// admit or spend a half-open probe. A provider without a configured circuit
// breaker counts as available ("closed"). The gateway is ready when at least one
// configured target is routable — see Readiness.Ready for why the threshold sits
// there and why it is measured over targets rather than over registrations.
func (g *Gateway) Readiness() Readiness {
	g.mu.RLock()
	defer g.mu.RUnlock()

	circuits := make(map[string]string, len(g.providerNames))
	provs := make([]ProviderReadiness, 0, len(g.providerNames))
	for _, name := range g.providerNames {
		circuit := circuitClosed
		if cb, ok := g.circuitBreakers[name]; ok {
			circuit = circuitStateString(cb.State())
		}
		circuits[name] = circuit
		provs = append(provs, ProviderReadiness{Name: name, Circuit: circuit})
	}

	targets := make([]TargetReadiness, 0, len(g.config.Targets))
	ready := false
	for _, t := range g.config.Targets {
		circuit, registered := circuits[t.VirtualKey]
		routable := registered && circuit != circuitOpen
		ready = ready || routable
		targets = append(targets, TargetReadiness{Name: t.VirtualKey, Routable: routable})
	}
	return Readiness{Ready: ready, Providers: provs, Targets: targets, MCPServers: g.mcpReadiness()}
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

// CircuitBreakerStates reports the current state of every configured circuit
// breaker, keyed by target name and encoded as pkg/metrics expects
// (0=closed 1=open 2=half_open).
//
// It is the scrape-time source for gateway_circuit_breaker_state; install it
// with metrics.SetCircuitBreakerStateSource. Resolving state here rather than
// writing the gauge from a request outcome is what keeps it honest. A breaker
// leaves Open on a timer, not on an event, so a pushed gauge reads "open" for
// the whole recovery window and forever once traffic stops — an alert on it
// could never clear itself.
//
// A target with a breaker appears from the moment it is configured, before it
// has served anything, so an absent series means no breaker is configured
// rather than one that has not been exercised yet.
//
// Reading is non-consuming: like Readiness it resolves state without admitting
// or spending a half-open probe.
func (g *Gateway) CircuitBreakerStates() map[string]float64 {
	g.mu.RLock()
	defer g.mu.RUnlock()

	out := make(map[string]float64, len(g.circuitBreakers))
	for name, cb := range g.circuitBreakers {
		out[name] = float64(cb.State())
	}
	return out
}

// PublishCircuitBreakerMetrics installs this gateway as the scrape-time source
// for gateway_circuit_breaker_state.
//
// It is an explicit call rather than something New does, because the source is
// process-global and a library embedder that builds more than one Gateway
// should decide which one the process reports on.
func (g *Gateway) PublishCircuitBreakerMetrics() {
	metrics.SetCircuitBreakerStateSource(g.CircuitBreakerStates)
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
