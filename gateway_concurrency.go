package aigateway

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/ferro-labs/ai-gateway/pkg/circuitbreaker"
	"github.com/ferro-labs/ai-gateway/providers"
)

// Per-target concurrency limiting: a semaphore applied at the call site, composed
// inside the circuit breaker.
//
// This deliberately is NOT a worker pool. The gateway's execution model is direct
// synchronous provider calls wrapped by thin call-site decorators (see cbProvider);
// a pre-spawned per-provider worker pool would layer a second, foreign execution
// model on top of it, add a goroutine hop and result marshalling to every request,
// and — because a pooled wrapper has to be stored in the registry — force the
// wrapper to re-expose every optional interface of the provider it wraps. A wrapper
// that advertises capabilities its inner provider lacks silently corrupts
// capability detection (model indexing, discovery, proxy eligibility), which is
// exactly the trap this design avoids: like cbProvider, limitedProvider embeds the
// base Provider interface only and is built per call, never stored.

// DefaultConcurrencyQueueSize is the number of requests allowed to wait for an
// in-flight slot when a target sets max_concurrency but omits queue_size.
const DefaultConcurrencyQueueSize = 1000

// MaxTargetConcurrency moved to the config package; it is re-exported as an
// alias in config_aliases.go.

// providerLimiter bounds how many requests may be in flight against a single
// target, and how many may wait for a slot.
type providerLimiter struct {
	slots   chan struct{} // capacity == max in-flight requests
	waiting atomic.Int64  // requests currently queued for a slot
	maxWait int64
}

// newProviderLimiter builds a limiter admitting maxConcurrency simultaneous
// requests with at most queueSize waiting behind them.
func newProviderLimiter(maxConcurrency, queueSize int) *providerLimiter {
	if queueSize <= 0 {
		queueSize = DefaultConcurrencyQueueSize
	}
	return &providerLimiter{
		slots:   make(chan struct{}, maxConcurrency),
		maxWait: int64(queueSize),
	}
}

// acquire takes an in-flight slot, queueing when the target is busy.
//
// It returns ErrProviderSaturated immediately when the queue is already full —
// callers get a fast, explicit backpressure signal rather than blocking forever —
// and sheds a request whose context ends while it is still queued, so a cancelled
// request never occupies a slot.
//
// Both sheds carry ErrProviderSaturated, because both are the gateway turning work
// away under its own concurrency limit rather than anything the target did. That
// distinction is what keeps the target's circuit breaker out of it: a request shed
// from the queue never reached the provider, and blaming it would open the breaker
// on a perfectly healthy target every time a burst outlasts request_timeout. The
// context error stays in the chain so callers still see why the wait ended.
func (l *providerLimiter) acquire(ctx context.Context) error {
	select {
	case l.slots <- struct{}{}:
		return nil // a slot was free: no queueing, no contention
	default:
	}

	if l.waiting.Add(1) > l.maxWait {
		l.waiting.Add(-1)
		return providers.ErrProviderSaturated
	}
	defer l.waiting.Add(-1)

	select {
	case l.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("%w: %w", providers.ErrProviderSaturated, ctx.Err())
	}
}

// release returns an in-flight slot.
func (l *providerLimiter) release() { <-l.slots }

// limitedProvider gates a provider's upstream calls through a per-target
// semaphore. Like cbProvider it embeds the base Provider interface only, and is
// constructed at the call site and never stored in the registry — so every
// capability type-assertion elsewhere in the gateway still sees the real provider.
type limitedProvider struct {
	providers.Provider
	lim  *providerLimiter
	name string
}

// Complete holds an in-flight slot for the duration of the upstream call.
func (p *limitedProvider) Complete(ctx context.Context, req providers.Request) (*providers.Response, error) {
	if err := p.lim.acquire(ctx); err != nil {
		return nil, err
	}
	defer p.lim.release()
	return p.Provider.Complete(ctx, req)
}

// CompleteStream holds the slot for the WHOLE stream, not merely its setup. The
// upstream connection stays occupied until the last chunk, so releasing the slot
// once response headers arrive would let unlimited streams run concurrently and
// defeat the cap entirely.
func (p *limitedProvider) CompleteStream(ctx context.Context, req providers.Request) (<-chan providers.StreamChunk, error) {
	sp, ok := p.Provider.(providers.StreamProvider)
	if !ok {
		return nil, fmt.Errorf("provider %s does not support streaming", p.name)
	}

	if err := p.lim.acquire(ctx); err != nil {
		return nil, err
	}
	upstream, err := sp.CompleteStream(ctx, req)
	if err != nil {
		p.lim.release()
		return nil, err
	}

	out := make(chan providers.StreamChunk)
	go func() {
		defer p.lim.release()
		defer close(out)
		for {
			select {
			case chunk, ok := <-upstream:
				if !ok {
					return
				}
				select {
				case out <- chunk:
				case <-ctx.Done():
					drainPending(upstream)
					return
				}
			case <-ctx.Done():
				// Selecting on the RECEIVE matters as much as on the send. A provider
				// that returns a channel and then never sends leaves the send arm
				// unreachable, so without this arm the forwarder sits blocked here and
				// the slot stays taken until the provider closes — which for a hung
				// upstream is never, and the whole target's queue waits behind it.
				drainPending(upstream)
				return
			}
		}
	}()
	return out, nil
}

// drainPending takes what upstream has ready — including a send a provider has
// already parked on it — so that provider's sender goroutine can finish and
// close its channel instead of blocking on a send nobody will receive.
//
// It never waits. Waiting is what the concurrency slot cannot afford: the slot
// is released only once this forwarder returns, and a provider that has hung is
// exactly the one that would never let an unbounded drain finish. Nothing is
// normally parked anyway — every streaming provider sends through
// core.SendChunk, which abandons a pending send on the same ctx that ended
// here — so this covers only the instant between a send being parked and that
// abandonment, and any provider that does not guard its sends at all.
func drainPending(upstream <-chan providers.StreamChunk) {
	for {
		select {
		case _, ok := <-upstream:
			if !ok {
				return
			}
		default:
			return
		}
	}
}

// decorateProvider composes the per-target decorators around p.
//
// The order is load-bearing: the concurrency limiter is INNERMOST so it gates only
// the upstream call, and the circuit breaker is OUTERMOST so an open circuit fails
// fast without ever occupying an in-flight slot or a queue position.
func decorateProvider(name string, p providers.Provider, cb *circuitbreaker.CircuitBreaker, lim *providerLimiter) providers.Provider {
	if lim != nil {
		p = &limitedProvider{Provider: p, lim: lim, name: name}
	}
	if cb != nil {
		p = &cbProvider{Provider: p, cb: cb, name: name}
	}
	return p
}

// The embeddings and image surfaces used to reach the limiter through a
// withTargetSlot helper here, because they resolved a capability interface out
// of the registry and could not be wrapped by decorateProvider. They now share
// the pipeline's callUnderResilience, which applies the limiter at the call site
// for every surface, so the helper had no callers left.

// ensureProviderLimitersLocked creates a concurrency limiter for every target that
// configures one. Caller must hold g.mu.
func (g *Gateway) ensureProviderLimitersLocked() {
	for _, t := range g.config.Targets {
		if t.Concurrency == nil || t.Concurrency.MaxConcurrency <= 0 {
			continue
		}
		if _, exists := g.limiters[t.VirtualKey]; exists {
			continue
		}
		g.limiters[t.VirtualKey] = newProviderLimiter(
			t.Concurrency.MaxConcurrency,
			t.Concurrency.QueueSize,
		)
	}
}
