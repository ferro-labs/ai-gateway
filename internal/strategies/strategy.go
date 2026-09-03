// Package strategies implements the routing strategies used by the gateway.
//
// Available strategies:
//   - Single:        always routes to one configured target.
//   - Fallback:      tries targets in order, retrying on failure.
//   - LoadBalance:   distributes requests across targets by weight.
//   - Conditional:   routes based on request field matching rules.
//   - LeastLatency:  routes to the target with the lowest observed p50 latency.
//   - CostOptimized: routes to the cheapest catalog-priced target.
//   - ContentBased:  routes based on the textual content of prompt messages.
//   - ABTest:        weighted random traffic splitting across labelled variants.
package strategies

import (
	"github.com/ferro-labs/ai-gateway/providers"
)

// Strategy defines the interface for routing strategies.
//
// A strategy decides ORDER, and that is the whole of it. It does not retry, it
// does not consult a circuit breaker, it does not classify a failure and it does
// not decide which capability a target needs — those belong to the gateway's
// request pipeline, which is the one place they can be identical on every
// surface. This mirrors how Envoy splits the same problem: the route table and
// the load balancer say where a request may go, while the retry policy hangs off
// the route and the circuit breaker off the cluster.
//
// The interface used to carry a second method, Execute, which fused selection
// with the provider call. That fusion is why retry worked only in the one mode
// that happened to implement a loop, and why the same config could order targets
// differently on two surfaces. Ordering is now stated once, here, and executed
// once, in the pipeline.
type Strategy interface {
	// SelectTargets returns the ordered target virtual keys the strategy would
	// try for req, most-preferred first, followed by the remaining configured
	// targets. Every routed surface walks this one ordering.
	//
	// The keys are exactly the candidates the walk may attempt, and the walk
	// treats them alike under every mode: it advances past a failover-safe
	// failure, skips a key whose circuit is open or that is parked after a
	// 429, and never reaches a key this method did not return. Pool modes
	// (`fallback`, `loadbalance`, `least-latency`, `cost-optimized`, `ab-test`)
	// return their whole ordered pool. A rule mode (`conditional`,
	// `content-based`) returns the matched rule's `target_keys` chain — a
	// one-entry chain for `target_key` — and nothing else, so a rule that names
	// one target is exact. `single` returns its one target. `targets[].retry`
	// asks each key repeatedly before the walk moves on, under every mode.
	//
	// Model support is not a reason for the walk to skip a key — a target the
	// strategy named while holding the request is a decision the pipeline
	// honours. See Gateway.eligibleKeys.
	//
	// A nil slice means no configured target serves the model at all, which the
	// caller reports as the caller's 404 rather than attempting anything.
	// Implementations may return a slice they own, so callers must not modify
	// it. Only CostOptimized returns an error: in skip mode when no priced
	// provider supports the model. All other strategies return a nil error.
	SelectTargets(req providers.Request) ([]string, error)
}

// ProviderLookup resolves a provider name to a Provider instance.
type ProviderLookup func(name string) (providers.Provider, bool)
