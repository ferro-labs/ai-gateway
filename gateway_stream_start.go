package aigateway

import (
	"context"
	"errors"
	"fmt"
	"runtime/trace"

	"github.com/ferro-labs/ai-gateway/pkg/circuitbreaker"
	"github.com/ferro-labs/ai-gateway/providers"
)

// startStreamWithStrategy runs the stream START through the same pipeline
// /v1/chat/completions uses, so target ordering, per-target retry, the circuit
// breaker, the concurrency limit and "nothing here can serve this" are the
// gateway's one implementation rather than streaming's own copy of it. It
// returns the target that answered — or, on failure, the last one attempted —
// alongside the raw provider channel.
//
// Only the START is the pipeline's. Everything after it belongs to whatever
// watches the channel drain, which is why the plan sets responseOutlivesCall:
// see targetPlan for what that hands over and why it must be handed over.
//
// If no configured target is viable for the model (wrong model, not a
// StreamProvider, not registered) the request is refused with
// core.ErrNoCapableProvider: targets is an allowlist, so a provider that is
// registered but not listed never serves a stream. A returned channel is never
// replayed.
//
// startCtx bounds this whole selection/retry phase only — it is what the
// pipeline and strategies.WaitBeforeRetry check for expiry. streamCtx is the
// context every CompleteStream call actually runs on and must stay free of that
// deadline: a provider keeps reading its response body on whatever context it
// was called with for as long as the returned channel is alive, so a
// start-phase timeout attached to streamCtx would tear down an
// already-successful stream the moment the clock ran out.
// The third return is the circuit breaker that admitted the successful start —
// the INSTANCE the probe was admitted on, taken off the decorated provider the
// pipeline actually called, never re-fetched from g.circuitBreakers by name. A
// ReloadConfig that retires and rebuilds a target's breaker while a stream is
// mid-start would otherwise hand the stream's eventual outcome to a fresh
// breaker whose state machine never admitted it. Nil when the target has no
// breaker configured.
func (g *Gateway) startStreamWithStrategy(startCtx, streamCtx context.Context, req providers.Request) (string, <-chan providers.StreamChunk, *circuitbreaker.CircuitBreaker, error) {
	g.mu.Lock()
	g.ensureCircuitBreakersLocked()
	g.ensureProviderLimitersLocked()
	g.mu.Unlock()

	keys, err := g.streamingTargetOrder(req)
	if err != nil {
		return "", nil, nil, err
	}

	plan := g.planFor(req.Model, keys)
	plan.responseOutlivesCall = true

	var admitted *circuitbreaker.CircuitBreaker
	raw, key, err := routeTargets(startCtx, g, plan, req, streamCapable, startStreamOn(streamCtx, &admitted))
	if err != nil {
		return key, nil, nil, err
	}
	return key, raw, admitted, nil
}

// streamCapable is streaming's candidacy gate: a target whose provider cannot
// stream is not a candidate and is never called.
func streamCapable(p providers.Provider) bool {
	_, ok := providers.As[providers.StreamProvider](p)
	return ok
}

// startStreamOn builds streaming's leaf call.
//
// It is the one surface whose leaf call is a closure rather than a package-level
// function, because the call needs TWO contexts and the pipeline supplies only
// one. The attempt context the pipeline passes bounds the WAIT for the provider
// to answer; streamCtx — captured here — is what the call itself runs on, and
// must outlive the attempt because the channel it returns does.
//
// admitted records the breaker wrapping each attempt's provider — nil when the
// attempt had none. The walk runs attempts sequentially and returns on the one
// that succeeds, so after a successful walk it holds the instance whose Allow()
// admitted the live stream, which is the only instance its outcome may resolve.
func startStreamOn(streamCtx context.Context, admitted **circuitbreaker.CircuitBreaker) targetCall[providers.Request, <-chan providers.StreamChunk] {
	return func(attemptCtx context.Context, p providers.Provider, req providers.Request) (<-chan providers.StreamChunk, error) {
		if cbp, ok := p.(*cbProvider); ok {
			*admitted = cbp.cb
		} else {
			*admitted = nil
		}
		// streamCapable already proved this of the undecorated provider, and
		// both decorators forward CompleteStream, so the assertion cannot fail.
		sp, ok := providers.As[providers.StreamProvider](p)
		if !ok {
			return nil, fmt.Errorf("provider %s does not support streaming", p.Name())
		}
		var (
			raw      <-chan providers.StreamChunk
			startErr error
		)
		trace.WithRegion(attemptCtx, "gateway.route_stream.provider.start", func() {
			raw, startErr = raceCompleteStream(attemptCtx, streamCtx, sp, req)
		})
		if startErr == nil && raw == nil {
			return nil, errors.New("provider returned a nil stream")
		}
		return raw, startErr
	}
}

// raceCompleteStream bounds only the wait for sp.CompleteStream to return. The
// call itself always runs on streamCtx; waitCtx is consulted solely to decide
// how long to keep waiting for a result. If waitCtx expires first, the attempt
// is abandoned and reported as a failure so the pipeline can retry or
// fall back, while the abandoned call keeps running on streamCtx and resolves
// independently — its result, and any circuit-breaker bookkeeping
// CompleteStream performs, land whenever the provider actually answers.
//
// An abandoned attempt that later succeeds hands back a live channel no caller
// will ever read. Its producer would then block forever on the first send,
// holding the provider connection until streamCtx is cancelled, so the
// abandoning path drains that channel to completion instead of dropping it —
// and owns whatever else that start is still holding (see below).
func raceCompleteStream(waitCtx, streamCtx context.Context, sp providers.StreamProvider, req providers.Request) (<-chan providers.StreamChunk, error) {
	type startResult struct {
		ch  <-chan providers.StreamChunk
		err error
	}
	done := make(chan startResult, 1)
	go func() {
		ch, err := sp.CompleteStream(streamCtx, req)
		done <- startResult{ch, err}
	}()
	select {
	case r := <-done:
		return r.ch, r.err
	case <-waitCtx.Done():
		go func() {
			r := <-done
			// This goroutine is now the only one that can see the abandoned
			// attempt, so it also owns the half-open circuit-breaker probe a
			// SUCCESSFUL start is still holding: the channel never reaches
			// RouteStream, so the CircuitBreakerOutcome that would normally
			// resolve it at stream completion is never installed. A failed start
			// needs nothing here — cbProvider.CompleteStream already recorded or
			// released it. The probe is released rather than recorded as a
			// success: a start that overran the gateway's own deadline is no
			// evidence the provider has recovered. Left held, it wedges the
			// target for good — resolveState only repairs Open→HalfOpen on a
			// timer, never a HalfOpen circuit stuck at its probe cap.
			if cbp, ok := sp.(*cbProvider); ok && r.err == nil {
				cbp.cb.ReleaseProbe()
			}
			if r.ch != nil {
				for range r.ch { //nolint:revive // drain so the provider's producer can finish
				}
			}
		}()
		return nil, context.Cause(waitCtx)
	}
}

// streamingTargetOrder resolves the strategy — the same object Route executes —
// and asks it for the streaming target order, so both paths share one ordering
// implementation. A getStrategy error surfaces identically on both paths; for
// ValidateConfig-passing gateways getStrategy does not error here.
func (g *Gateway) streamingTargetOrder(req providers.Request) ([]string, error) {
	s, err := g.getStrategy()
	if err != nil {
		return nil, err
	}
	return s.SelectTargets(req)
}

func responseStream(resp *providers.Response) <-chan providers.StreamChunk {
	ch := make(chan providers.StreamChunk, 1)
	streamChoices := make([]providers.StreamChoice, len(resp.Choices))
	for i, c := range resp.Choices {
		streamChoices[i] = providers.StreamChoice{
			Index: c.Index,
			Delta: providers.MessageDelta{
				Role:      c.Message.Role,
				Content:   c.Message.Content,
				ToolCalls: c.Message.ToolCalls,
			},
			FinishReason: c.FinishReason,
		}
	}
	ch <- providers.StreamChunk{
		ID:      resp.ID,
		Object:  "chat.completion.chunk",
		Created: resp.Created,
		Model:   resp.Model,
		Choices: streamChoices,
		Usage:   &resp.Usage,
	}
	close(ch)
	return ch
}

// Target resolution — registered, serves the model, can stream, and decorated
// with its breaker and limiter — is the pipeline's resolveTarget now, gated by
// streamCapable above.
