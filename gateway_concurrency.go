package aigateway

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/ferro-labs/ai-gateway/internal/circuitbreaker"
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

// MaxTargetConcurrency is the highest value ValidateConfig accepts for a target's
// max_concurrency or queue_size.
//
// The bound is about intent, not memory: slots is a chan struct{}, whose zero-size
// element means capacity costs no buffer at all. What an absurd value does instead
// is admit every request, so the cap the operator asked for silently stops applying.
// Real per-target concurrency is bounded by what the upstream provider will accept —
// orders of magnitude below this — so a larger value is a typo, not a deployment.
const MaxTargetConcurrency = 10_000

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
// and ctx.Err() when the caller goes away while waiting, so a cancelled request
// never occupies a slot.
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
		return ctx.Err()
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
		for chunk := range upstream {
			select {
			case out <- chunk:
			case <-ctx.Done():
				// The consumer abandoned the stream. Keep draining upstream so the
				// provider's sender goroutine can finish and close its channel rather
				// than blocking forever on a send nobody will receive, then release
				// the slot.
				//nolint:revive // empty-block: consuming the remaining chunks IS the work
				for range upstream {
				}
				return
			}
		}
	}()
	return out, nil
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

// withTargetSlot runs fn under target's concurrency limiter, gating only the
// upstream call — never the surrounding plugin-governance pipeline, so a
// budget or rate-limit rejection never holds a slot. It returns fn(ctx)
// directly when target has no limiter configured.
//
// This is the call-site equivalent of limitedProvider.Complete for surfaces
// (Embed, GenerateImage) that resolve a capability interface directly out of
// the registry instead of through decorateProvider: those values must not be
// wrapped, since a wrapper embedding only providers.Provider would fail the
// EmbeddingProvider / ImageProvider type assertion the caller already made.
func (g *Gateway) withTargetSlot(ctx context.Context, target string, fn func(context.Context) error) error {
	g.mu.RLock()
	lim := g.limiters[target]
	g.mu.RUnlock()

	if lim == nil {
		return fn(ctx)
	}
	if err := lim.acquire(ctx); err != nil {
		return err
	}
	defer lim.release()
	return fn(ctx)
}

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
