package aigateway

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/strategies"
	"github.com/ferro-labs/ai-gateway/observability"
	"github.com/ferro-labs/ai-gateway/pkg/circuitbreaker"
	"github.com/ferro-labs/ai-gateway/providers"
)

// errAttemptTimeout is the cause placed on an attempt context whose
// targets[].timeout elapsed. It wraps context.DeadlineExceeded so the error
// classifies as the 504 an upstream that never answered already does.
var errAttemptTimeout = fmt.Errorf("target attempt timeout: %w", context.DeadlineExceeded)

// attemptTarget runs one target's retry policy, calling it under its breaker
// and limiter on every try. attemptsActive is whether obs has opted into one
// SubjectRoutingAttempt event per call; when it has not, no event is built.
// spanner, when non-nil, opens one SpanNameRoutingAttempt span per call and
// the provider call runs under it, so the outbound HTTP span nests beneath.
func attemptTarget[Req, Resp any](
	ctx context.Context,
	g *Gateway,
	obs observability.Provider,
	attemptsActive bool,
	spanner observability.AttemptSpanProvider,
	target routedTarget,
	routedModel string,
	attemptSequence *int,
	p providers.Provider,
	cb *circuitbreaker.CircuitBreaker,
	lim *providerLimiter,
	req Req,
	upstreamModel string,
	call targetCall[Req, Resp],
) (Resp, error) {
	var zero Resp
	policy := g.retryPolicyFor(target.key)

	var lastErr error
	for attempt := 0; attempt < policy.attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		if attempt > 0 {
			proceed, err := strategies.WaitBeforeRetry(ctx, attempt, policy.initialBackoffMs, lastErr)
			if err != nil {
				return zero, err
			}
			// Both outcomes are logged: a retry that happened and a target
			// abandoned because its Retry-After was longer than the gateway is
			// willing to hold a request open are each invisible otherwise, and
			// "did my retry config do anything" is the first question asked of
			// this block.
			if !proceed {
				g.log.Ctx(ctx).Info("abandoning target: Retry-After exceeds the cap",
					"target", target.key, "retry_after", providers.RetryAfterFrom(lastErr))
				break
			}
			g.log.Ctx(ctx).Info("retrying target", "target", target.key, "attempt", attempt+1)
		}
		started := time.Now()
		// targets[].timeout bounds THIS attempt. The deadline lives on a child
		// context, so the request's own deadline stays authoritative and an
		// attempt that times out reads as a target failure — failover-safe,
		// and never mistaken for the caller giving up (shouldAdvanceTarget
		// consults the parent). For a stream the child bounds only the wait
		// for the provider to answer: the stream itself runs on the context
		// startStreamOn captured, so a stream that began in time is not cut
		// off by its attempt deadline.
		attemptCtx, cancelAttempt := ctx, func() {}
		if policy.attemptTimeout > 0 {
			attemptCtx, cancelAttempt = context.WithTimeoutCause(ctx, policy.attemptTimeout, errAttemptTimeout)
		}
		callCtx := attemptCtx
		var attemptSpan observability.Span
		if spanner != nil {
			callCtx, attemptSpan = spanner.StartAttemptSpan(attemptCtx, target.key, *attemptSequence+1)
		}
		resp, err := callUnderResilience(callCtx, target.key, p, cb, lim, req, upstreamModel, call)
		cancelAttempt()
		if err != nil && ctx.Err() == nil && errors.Is(context.Cause(attemptCtx), errAttemptTimeout) {
			err = fmt.Errorf("target %s: attempt timed out after %s: %w: %w", target.key, policy.attemptTimeout, errAttemptTimeout, err)
		}
		if attemptSpan != nil {
			endAttemptSpan(attemptSpan, err)
		}
		(*attemptSequence)++
		g.recordRoutingAttempt(ctx, obs, attemptsActive, routingAttempt{
			target:         target,
			routedModel:    routedModel,
			sequence:       *attemptSequence,
			targetSequence: attempt + 1,
		}, time.Since(started), err)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if !strategies.ShouldRetry(err, policy.onStatusCodes) {
			break
		}
	}
	return zero, lastErr
}

// endAttemptSpan closes one attempt's span with its outcome. The error goes
// through Span.SetError, so the provider's privacy level and redaction apply
// exactly as they do on the request span. For a stream the attempt ends when
// the stream starts, matching RoutingAttempt: a failure after the first byte
// belongs to the request span.
func endAttemptSpan(span observability.Span, err error) {
	outcome := observability.RoutingAttemptSuccess
	if err != nil {
		outcome = observability.RoutingAttemptError
		span.SetError(err)
	}
	span.SetAttribute(observability.AttrFerroRoutingOutcome, string(outcome))
	span.End()
}

// callUnderResilience performs one attempt with the target's circuit breaker
// outermost and its concurrency limiter innermost, so an open circuit fails
// fast without ever taking an in-flight slot or a queue position.
//
// The decorators are applied at the CALL SITE rather than by wrapping the
// provider. A wrapper embedding providers.Provider fails the
// EmbeddingProvider / ImageProvider type assertions the non-chat surfaces make,
// so a wrapper-based breaker can only ever cover chat — which is how the
// gateway ended up with two implementations of the same policy. Composition and
// semantics are identical to the wrapper pair (cbProvider, limitedProvider) it
// replaces on this path.
//
// The deferred recover is load-bearing: Allow() may have admitted a half-open
// probe, and a panicking probe that never resolves wedges the target for good —
// resolveState only repairs Open→HalfOpen on a timer, never a HalfOpen circuit
// stuck at its probe cap. The panic is recorded as a failure and re-raised, not
// swallowed.
func callUnderResilience[Req, Resp any](
	ctx context.Context,
	key string,
	p providers.Provider,
	cb *circuitbreaker.CircuitBreaker,
	lim *providerLimiter,
	req Req,
	upstreamModel string,
	call targetCall[Req, Resp],
) (resp Resp, err error) {
	if cb == nil && lim == nil {
		return call(ctx, p, req, upstreamModel)
	}

	if cb != nil {
		if !cb.Allow() {
			var zero Resp
			return zero, circuitbreaker.ErrCircuitOpen
		}
		defer func() {
			if r := recover(); r != nil {
				cb.RecordFailure()
				panic(r)
			}
			recordCircuitBreakerOutcome(ctx, cb, key, err)
		}()
	}

	if lim != nil {
		if acquireErr := lim.acquire(ctx); acquireErr != nil {
			var zero Resp
			return zero, acquireErr
		}
		defer lim.release()
	}

	return call(ctx, p, req, upstreamModel)
}
