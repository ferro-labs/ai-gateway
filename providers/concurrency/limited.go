// Package concurrency provides a transparent Provider wrapper that gates
// concurrent upstream calls to a configurable worker pool + queue.
//
// Pattern mirrors Bifrost (github.com/maximhq/bifrost): N worker goroutines
// drain a buffered channel of jobs. The channel buffer is the queue; the
// goroutine count is the concurrency cap per operation type. No explicit
// semaphore is needed — the bounded goroutine pool + bounded channel act as a
// two-level gate.
package concurrency

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// ErrQueueFull is returned when the provider queue is at capacity and the
// request cannot be accepted without blocking.
var ErrQueueFull = errors.New("provider concurrency queue full")

type completionResult struct {
	resp *core.Response
	err  error
}

type completionJob struct {
	ctx      context.Context
	req      core.Request
	response chan completionResult
}

type streamResult struct {
	ch  <-chan core.StreamChunk
	err error
}

type streamJob struct {
	ctx      context.Context
	req      core.Request
	response chan streamResult
}

type embedResult struct {
	resp *core.EmbeddingResponse
	err  error
}

type embedJob struct {
	ctx      context.Context
	req      core.EmbeddingRequest
	response chan embedResult
}

// LimitedProvider wraps a Provider and enforces per-provider concurrency limits.
// It implements Provider (and optionally StreamProvider / EmbeddingProvider /
// ProxiableProvider / DiscoveryProvider / ImageProvider) transparently —
// callers do not need to know a limit is in place.
type LimitedProvider struct {
	inner core.Provider

	completeQueue chan completionJob
	streamQueue   chan streamJob
	embedQueue    chan embedJob

	closing atomic.Bool
	done    chan struct{}
	once    sync.Once
}

// Wrap creates a LimitedProvider around inner with the given concurrency and
// queue sizes. workerCount is the number of worker goroutines per operation
// type (complete / stream / embed each get their own pool of workerCount
// goroutines). queueSize is the channel buffer depth per queue.
// Call Close() to shut down workers cleanly.
func Wrap(inner core.Provider, workerCount, queueSize int) *LimitedProvider {
	lp := &LimitedProvider{
		inner:         inner,
		completeQueue: make(chan completionJob, queueSize),
		done:          make(chan struct{}),
	}
	if _, ok := inner.(core.StreamProvider); ok {
		lp.streamQueue = make(chan streamJob, queueSize)
	}
	if _, ok := inner.(core.EmbeddingProvider); ok {
		lp.embedQueue = make(chan embedJob, queueSize)
	}

	for range workerCount {
		go lp.completionWorker()
	}
	if lp.streamQueue != nil {
		for range workerCount {
			go lp.streamWorker()
		}
	}
	if lp.embedQueue != nil {
		for range workerCount {
			go lp.embedWorker()
		}
	}
	return lp
}

// Close signals all workers to stop. Jobs already enqueued are drained and
// rejected with a shutdown error. New calls after Close() return immediately.
func (lp *LimitedProvider) Close() {
	lp.once.Do(func() {
		lp.closing.Store(true)
		close(lp.done)
	})
}

// --- Provider interface ---

// Name returns the wrapped provider's name.
func (lp *LimitedProvider) Name() string { return lp.inner.Name() }

// SupportedModels returns the wrapped provider's supported model IDs.
func (lp *LimitedProvider) SupportedModels() []string { return lp.inner.SupportedModels() }

// SupportsModel reports whether the wrapped provider supports model m.
func (lp *LimitedProvider) SupportsModel(m string) bool { return lp.inner.SupportsModel(m) }

// Models returns the wrapped provider's model metadata.
func (lp *LimitedProvider) Models() []core.ModelInfo { return lp.inner.Models() }

// Complete runs a completion through the worker pool, returning ErrQueueFull
// when the queue is at capacity.
func (lp *LimitedProvider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	if lp.closing.Load() {
		return nil, errors.New("provider shutting down")
	}
	job := completionJob{
		ctx:      ctx,
		req:      req,
		response: make(chan completionResult, 1),
	}
	select {
	case lp.completeQueue <- job:
	case <-lp.done:
		return nil, errors.New("provider shutting down")
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
		return nil, ErrQueueFull
	}
	select {
	case res := <-job.response:
		return res.resp, res.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// --- StreamProvider ---

// CompleteStream runs a streaming completion through the worker pool. It errors
// when the inner provider does not stream or the queue is at capacity.
func (lp *LimitedProvider) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
	if lp.streamQueue == nil {
		return nil, errors.New("inner provider does not support streaming")
	}
	if lp.closing.Load() {
		return nil, errors.New("provider shutting down")
	}
	job := streamJob{
		ctx:      ctx,
		req:      req,
		response: make(chan streamResult, 1),
	}
	select {
	case lp.streamQueue <- job:
	case <-lp.done:
		return nil, errors.New("provider shutting down")
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
		return nil, ErrQueueFull
	}
	select {
	case res := <-job.response:
		return res.ch, res.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// --- EmbeddingProvider ---

// Embed runs an embedding request through the worker pool. It errors when the
// inner provider does not embed or the queue is at capacity.
func (lp *LimitedProvider) Embed(ctx context.Context, req core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	if lp.embedQueue == nil {
		return nil, errors.New("inner provider does not support embeddings")
	}
	if lp.closing.Load() {
		return nil, errors.New("provider shutting down")
	}
	job := embedJob{
		ctx:      ctx,
		req:      req,
		response: make(chan embedResult, 1),
	}
	select {
	case lp.embedQueue <- job:
	case <-lp.done:
		return nil, errors.New("provider shutting down")
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
		return nil, ErrQueueFull
	}
	select {
	case res := <-job.response:
		return res.resp, res.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// --- ProxiableProvider (forwarded directly — not queue-gated) ---

// BaseURL forwards to the inner provider when it is proxiable, else "".
func (lp *LimitedProvider) BaseURL() string {
	if pp, ok := lp.inner.(core.ProxiableProvider); ok {
		return pp.BaseURL()
	}
	return ""
}

// AuthHeaders forwards to the inner provider when it is proxiable, else nil.
func (lp *LimitedProvider) AuthHeaders() map[string]string {
	if pp, ok := lp.inner.(core.ProxiableProvider); ok {
		return pp.AuthHeaders()
	}
	return nil
}

// --- DiscoveryProvider (forwarded directly — not queue-gated) ---

// DiscoverModels forwards to the inner provider when it supports discovery.
func (lp *LimitedProvider) DiscoverModels(ctx context.Context) ([]core.ModelInfo, error) {
	if dp, ok := lp.inner.(core.DiscoveryProvider); ok {
		return dp.DiscoverModels(ctx)
	}
	return nil, errors.New("inner provider does not support model discovery")
}

// --- ImageProvider (forwarded directly — not queue-gated) ---

// GenerateImage forwards to the inner provider when it supports image generation.
func (lp *LimitedProvider) GenerateImage(ctx context.Context, req core.ImageRequest) (*core.ImageResponse, error) {
	if ip, ok := lp.inner.(core.ImageProvider); ok {
		return ip.GenerateImage(ctx, req)
	}
	return nil, errors.New("inner provider does not support image generation")
}

// --- Workers ---

func (lp *LimitedProvider) completionWorker() {
	for {
		select {
		case job := <-lp.completeQueue:
			resp, err := lp.inner.Complete(job.ctx, job.req)
			job.response <- completionResult{resp: resp, err: err}
		case <-lp.done:
			for {
				select {
				case job := <-lp.completeQueue:
					job.response <- completionResult{err: errors.New("provider shutting down")}
				default:
					return
				}
			}
		}
	}
}

func (lp *LimitedProvider) streamWorker() {
	sp := lp.inner.(core.StreamProvider)
	for {
		select {
		case job := <-lp.streamQueue:
			ch, err := sp.CompleteStream(job.ctx, job.req)
			job.response <- streamResult{ch: ch, err: err}
		case <-lp.done:
			for {
				select {
				case job := <-lp.streamQueue:
					job.response <- streamResult{err: errors.New("provider shutting down")}
				default:
					return
				}
			}
		}
	}
}

func (lp *LimitedProvider) embedWorker() {
	ep := lp.inner.(core.EmbeddingProvider)
	for {
		select {
		case job := <-lp.embedQueue:
			resp, err := ep.Embed(job.ctx, job.req)
			job.response <- embedResult{resp: resp, err: err}
		case <-lp.done:
			for {
				select {
				case job := <-lp.embedQueue:
					job.response <- embedResult{err: errors.New("provider shutting down")}
				default:
					return
				}
			}
		}
	}
}
