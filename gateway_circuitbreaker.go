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

func (p *cbProvider) Complete(ctx context.Context, req providers.Request) (*providers.Response, error) {
	if !p.cb.Allow() {
		metrics.CircuitBreakerState.WithLabelValues(p.name).Set(1) // open
		return nil, circuitbreaker.ErrCircuitOpen
	}
	resp, err := p.Provider.Complete(ctx, req)
	if err != nil {
		if shouldRecordCircuitBreakerFailure(ctx, err) {
			p.cb.RecordFailure()
			metrics.CircuitBreakerState.WithLabelValues(p.name).Set(float64(p.cb.State()))
		} else {
			p.cb.ReleaseProbe()
		}
		return nil, err
	}
	p.cb.RecordSuccess()
	metrics.CircuitBreakerState.WithLabelValues(p.name).Set(float64(p.cb.State()))
	return resp, nil
}

func (p *cbProvider) CompleteStream(ctx context.Context, req providers.Request) (<-chan providers.StreamChunk, error) {
	if !p.cb.Allow() {
		metrics.CircuitBreakerState.WithLabelValues(p.name).Set(1) // open
		return nil, circuitbreaker.ErrCircuitOpen
	}
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

// recordStreamCircuitBreakerOutcome updates breaker state when a stream
// finishes. Startup failures are recorded in cbProvider.CompleteStream;
// this handles stream completion only.
func recordStreamCircuitBreakerOutcome(ctx context.Context, cb *circuitbreaker.CircuitBreaker, name string, err error) {
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
