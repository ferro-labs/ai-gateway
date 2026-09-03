package aigateway

import (
	"github.com/ferro-labs/ai-gateway/internal/strategies"
	"time"
)

// retryPolicy is one target's resolved retry configuration.
type retryPolicy struct {
	attempts         int
	onStatusCodes    []int
	initialBackoffMs int
	// attemptTimeout bounds one physical attempt (targets[].timeout); zero
	// means no bound beyond the request's own deadline.
	attemptTimeout time.Duration
}

// retryPolicyFor resolves targetKey's `retry` block.
//
// It is the ONE place the gateway decides how many times a target is tried, and
// it does not consult the routing mode. `targets[].retry` used to be honoured
// only under `mode: fallback`, so the same block meant three attempts on one
// mode and one attempt on the other seven, with nothing logged and no caveat in
// the shipped example — which documents `weight`'s mode limitation right above
// it. Retry and fallback are different questions and now answer separately:
// RETRY is how many times ONE target is asked, FALLBACK is whether a SECOND
// target is asked at all. Only the second is the strategy's business, which is
// how Envoy splits it too — its retry policy hangs off the route, not off the
// cluster's load-balancing policy.
//
// Backoff normalisation lives once, in strategies.NormalizeBackoffMs, so an
// unset initial_backoff_ms gets a jittered exponential wait rather than
// hammering the provider with immediate retries.
func (g *Gateway) retryPolicyFor(targetKey string) retryPolicy {
	g.mu.RLock()
	var (
		attempts       int
		onStatusCodes  []int
		backoffMs      int
		attemptTimeout time.Duration
	)
	for i := range g.config.Targets {
		if g.config.Targets[i].VirtualKey != targetKey {
			continue
		}
		if r := g.config.Targets[i].Retry; r != nil {
			attempts = r.Attempts
			onStatusCodes = r.OnStatusCodes
			backoffMs = r.InitialBackoffMs
		}
		// Parsed only when set: ParseDuration("") allocates an error, and this
		// runs per attempt. Validated at load, so a parse failure here is a
		// programmatic Config that skipped ValidateConfig; it reads as no
		// bound rather than a panic.
		if t := g.config.Targets[i].Timeout; t != "" {
			attemptTimeout, _ = time.ParseDuration(t)
		}
		break
	}
	g.mu.RUnlock()

	if attempts <= 0 {
		attempts = 1
	}
	return retryPolicy{
		attempts:         attempts,
		onStatusCodes:    onStatusCodes,
		initialBackoffMs: strategies.NormalizeBackoffMs(backoffMs),
		attemptTimeout:   max(attemptTimeout, 0),
	}
}
