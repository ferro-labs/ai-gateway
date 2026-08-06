package aigateway

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ferro-labs/ai-gateway/pkg/circuitbreaker"
	"github.com/ferro-labs/ai-gateway/pkg/metrics"
	"github.com/ferro-labs/ai-gateway/providers"
)

// cbProvider and its helpers wrap a Provider with a per-provider circuit
// breaker for the gateway's routing paths.

// cbProvider wraps a Provider with a circuit breaker.
type cbProvider struct {
	providers.Provider
	cb   *circuitbreaker.CircuitBreaker
	name string
}

func (p *cbProvider) Complete(ctx context.Context, req providers.Request) (resp *providers.Response, err error) {
	if !p.cb.Allow() {
		return nil, circuitbreaker.ErrCircuitOpen
	}
	// Deferred so a panic from p.Provider.Complete still releases the
	// half-open probe Allow() just admitted. Without this, a panicking probe
	// leaks halfOpenProbes forever: resolveState() only turns Open into
	// HalfOpen on a timeout, it never repairs a HalfOpen circuit stuck at its
	// probe cap, so Allow() would reject every request for this provider
	// until the process restarts. A panic is treated as a failure, then
	// re-raised so it still propagates to the caller.
	defer func() {
		if r := recover(); r != nil {
			p.cb.RecordFailure()
			panic(r)
		}
		recordCircuitBreakerOutcome(ctx, p.cb, p.name, err)
	}()
	resp, err = p.Provider.Complete(ctx, req)
	return resp, err
}

func (p *cbProvider) CompleteStream(ctx context.Context, req providers.Request) (<-chan providers.StreamChunk, error) {
	if !p.cb.Allow() {
		return nil, circuitbreaker.ErrCircuitOpen
	}
	// Deferred for the same reason as Complete: a panic out of CompleteStream
	// would otherwise strand the half-open probe Allow() just admitted and
	// reject every later request for this provider until restart. Only the
	// panic path is handled here — a stream that starts is not yet a success,
	// so the probe stays held and the outcome is reported at stream completion
	// via MeterMeta.CircuitBreakerOutcome.
	defer func() {
		if r := recover(); r != nil {
			p.cb.RecordFailure()
			panic(r)
		}
	}()
	sp, ok := p.Provider.(providers.StreamProvider)
	if !ok {
		p.cb.ReleaseProbe()
		return nil, fmt.Errorf("provider %s does not support streaming", p.name)
	}
	ch, err := sp.CompleteStream(ctx, req)
	if err != nil {
		if shouldRecordCircuitBreakerFailure(ctx, err) {
			p.cb.RecordFailure()
		} else {
			p.cb.ReleaseProbe()
		}
		return nil, err
	}
	return ch, nil
}

// shouldRecordCircuitBreakerFailure reports whether an error should count toward
// opening the circuit.
//
// The distinction that matters is WHOSE fault the failure is — the same question
// the error_type metric label answers, which is why both are decided by the one
// classifier (metrics.ProviderErrorType) rather than by two switches that drift:
//
//   - The gateway's own request deadline (Config.RequestTimeout) firing, and the
//     streaming idle bound (streamio.ErrIdleTimeout), both mean the provider was
//     too slow to answer. Those classify as ErrTypeTimeout, are the provider's
//     fault, and MUST trip the breaker. Treating either as caller cancellation
//     would leave a hung provider in rotation forever while /readyz — whose only
//     provider signal is circuit state — kept reporting the pod ready.
//   - A caller-side cancellation or a caller-supplied deadline classifies as
//     ErrTypeClientCanceled and is excluded, so transient client behavior cannot
//     block healthy traffic.
//   - Shedding under our own per-target concurrency limit classifies as
//     ErrTypeBackpressure: a 429 the provider never saw.
//   - A rejection the gateway raised itself before ever reaching the provider (an
//     unsupported parameter under compatibility.on_unsupported_param=reject) is a
//     client error that never touched the network, and must never blame the
//     provider. It has no error_type of its own, so it is named here.
//   - Rate limits are expected and temporary, and stay excluded.
func shouldRecordCircuitBreakerFailure(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}

	var unsupportedParam *providers.UnsupportedParamError
	if errors.As(err, &unsupportedParam) {
		return false
	}

	switch metrics.ProviderErrorType(ctx, err) {
	case metrics.ErrTypeBackpressure, metrics.ErrTypeClientCanceled:
		return false
	}
	return !isRateLimitError(err)
}

// recordCircuitBreakerOutcome updates breaker state from the result of one
// upstream call: a blameworthy failure trips the breaker, a failure that is not
// the provider's fault releases the half-open probe instead, and a success
// closes it. Used by the stream path once a stream finishes (its startup
// failures are recorded in cbProvider.CompleteStream) and by withTargetBreaker
// for the surfaces that cannot be wrapped.
//
// It records the outcome and nothing else. The gateway_circuit_breaker_state
// gauge is not written here — it is resolved from the live breakers on each
// scrape (see Gateway.CircuitBreakerStates), because a breaker also changes
// state on a timer that no request outcome observes. The target name that used
// to label that write is now unused; the parameter stays so the call sites on
// the streaming and pipeline paths are untouched by this change.
func recordCircuitBreakerOutcome(ctx context.Context, cb *circuitbreaker.CircuitBreaker, _ string, err error) {
	if err != nil {
		if !shouldRecordCircuitBreakerFailure(ctx, err) {
			cb.ReleaseProbe()
			return
		}
		cb.RecordFailure()
		return
	}
	cb.RecordSuccess()
}

// withTargetBreaker runs fn under the target's circuit breaker, for the surfaces
// cbProvider cannot wrap. Embedding and image providers are reached through
// optional interfaces (EmbeddingProvider / ImageProvider), and a wrapper
// embedding providers.Provider would fail those type assertions and break the
// surface outright — the same constraint that puts the concurrency limiter at
// the call site. A target with no breaker configured runs fn unchanged.
//
// Composition mirrors decorateProvider: the breaker is OUTERMOST and the
// limiter INNERMOST, so an open circuit fails fast without ever taking an
// in-flight slot or a queue position.
func (g *Gateway) withTargetBreaker(ctx context.Context, target string, fn func(context.Context) error) error {
	g.mu.RLock()
	cb := g.circuitBreakers[target]
	g.mu.RUnlock()

	if cb == nil {
		return fn(ctx)
	}
	if !cb.Allow() {
		return circuitbreaker.ErrCircuitOpen
	}
	// Deferred for the same reason as cbProvider.Complete: fn panicking must
	// still resolve the half-open probe Allow() admitted, or the breaker gets
	// stuck rejecting this target forever with no self-healing. A panic
	// counts as a failure and is re-raised afterward, never swallowed.
	defer func() {
		if r := recover(); r != nil {
			cb.RecordFailure()
			panic(r)
		}
	}()
	err := fn(ctx)
	recordCircuitBreakerOutcome(ctx, cb, target, err)
	return err
}

// ensureCircuitBreakersLocked creates circuit breakers for configured targets.
// Caller must hold g.mu.
func (g *Gateway) ensureCircuitBreakersLocked() {
	for _, t := range g.config.Targets {
		if t.CircuitBreaker == nil {
			continue
		}
		if _, exists := g.circuitBreakers[t.VirtualKey]; exists {
			continue
		}
		// circuitbreaker.New reads an unparseable duration as zero and applies its
		// 30s default, so without this the target reopens on a schedule nobody
		// configured and nothing ever says why.
		timeout, err := time.ParseDuration(t.CircuitBreaker.Timeout)
		if err != nil && t.CircuitBreaker.Timeout != "" {
			g.log.Warn("target circuit_breaker.timeout is not a duration; applying the default",
				"target", t.VirtualKey, "timeout", t.CircuitBreaker.Timeout)
		}
		g.circuitBreakers[t.VirtualKey] = circuitbreaker.New(
			t.CircuitBreaker.FailureThreshold,
			t.CircuitBreaker.SuccessThreshold,
			t.CircuitBreaker.MaxHalfThreshold,
			timeout,
		)
	}
}

// isRateLimitError checks if the error is a 429 rate limit response.
// Rate limits are expected and temporary — they should not trip the circuit breaker.
func isRateLimitError(err error) bool {
	return providers.ParseStatusCode(err) == 429
}
