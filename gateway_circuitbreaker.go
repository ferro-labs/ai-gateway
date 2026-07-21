package aigateway

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/circuitbreaker"
	"github.com/ferro-labs/ai-gateway/internal/metrics"
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
		metrics.CircuitBreakerState.WithLabelValues(p.name).Set(1) // open
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
			metrics.CircuitBreakerState.WithLabelValues(p.name).Set(float64(p.cb.State()))
			panic(r)
		}
		recordCircuitBreakerOutcome(ctx, p.cb, p.name, err)
	}()
	resp, err = p.Provider.Complete(ctx, req)
	return resp, err
}

func (p *cbProvider) CompleteStream(ctx context.Context, req providers.Request) (<-chan providers.StreamChunk, error) {
	if !p.cb.Allow() {
		metrics.CircuitBreakerState.WithLabelValues(p.name).Set(1) // open
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
			metrics.CircuitBreakerState.WithLabelValues(p.name).Set(float64(p.cb.State()))
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
			metrics.CircuitBreakerState.WithLabelValues(p.name).Set(float64(p.cb.State()))
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
// The distinction that matters is WHOSE fault the failure is:
//
//   - The gateway's own request deadline (Config.RequestTimeout) firing means the
//     provider was too slow to answer. That is a provider failure and MUST trip the
//     breaker. Treating it as caller cancellation would leave a hung provider in
//     rotation forever while /readyz — whose only provider signal is circuit state —
//     kept reporting the pod ready.
//   - A caller-side cancellation or a caller-supplied deadline is not the provider's
//     fault and is excluded, so transient client behavior cannot block healthy traffic.
//   - A rejection the gateway raised itself before ever reaching the provider (an
//     unsupported parameter under compatibility.on_unsupported_param=reject) is a
//     client error that never touched the network, and must never blame the provider.
//   - Rate limits are expected and temporary, and stay excluded.
func shouldRecordCircuitBreakerFailure(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}

	// Errors the gateway produced itself, without ever calling the provider: an
	// unsupported-parameter rejection, and shedding under our own concurrency
	// limit. Neither is evidence that the upstream is unhealthy.
	var unsupportedParam *providers.UnsupportedParamError
	if errors.As(err, &unsupportedParam) || errors.Is(err, providers.ErrProviderSaturated) {
		return false
	}

	// The gateway's own deadline fired: the provider was too slow. context.Cause
	// carries ErrRequestTimeout only for a deadline this gateway installed; a
	// caller-supplied deadline or cancellation carries the stdlib sentinels.
	if errors.Is(context.Cause(ctx), ErrRequestTimeout) {
		return !isRateLimitError(err)
	}

	if ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
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
func recordCircuitBreakerOutcome(ctx context.Context, cb *circuitbreaker.CircuitBreaker, name string, err error) {
	if err != nil {
		if !shouldRecordCircuitBreakerFailure(ctx, err) {
			cb.ReleaseProbe()
			return
		}
		cb.RecordFailure()
		metrics.CircuitBreakerState.WithLabelValues(name).Set(float64(cb.State()))
		return
	}
	cb.RecordSuccess()
	metrics.CircuitBreakerState.WithLabelValues(name).Set(float64(cb.State()))
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
		metrics.CircuitBreakerState.WithLabelValues(target).Set(1) // open
		return circuitbreaker.ErrCircuitOpen
	}
	// Deferred for the same reason as cbProvider.Complete: fn panicking must
	// still resolve the half-open probe Allow() admitted, or the breaker gets
	// stuck rejecting this target forever with no self-healing. A panic
	// counts as a failure and is re-raised afterward, never swallowed.
	defer func() {
		if r := recover(); r != nil {
			cb.RecordFailure()
			metrics.CircuitBreakerState.WithLabelValues(target).Set(float64(cb.State()))
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
		timeout, _ := time.ParseDuration(t.CircuitBreaker.Timeout)
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
