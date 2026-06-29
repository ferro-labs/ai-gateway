// Package concurrency provides a transparent Provider wrapper that gates
// concurrent upstream calls to a configurable worker pool + queue.
//
// Pattern mirrors Bifrost (github.com/maximhq/bifrost): N worker goroutines
// drain a buffered channel of jobs. The channel buffer is the queue; the
// goroutine count is the concurrency cap. No explicit semaphore is needed —
// the bounded goroutine pool + bounded channel act as a two-level gate.
package concurrency

import (
	"context"
	"errors"
	"sync"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// ErrQueueFull is returned when the provider queue is at capacity and the
// request cannot be accepted without blocking indefinitely.
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
// It implements Provider (and optionally StreamProvider / EmbeddingProvider)
// transparently — callers do not need to know a limit is in place.
type LimitedProvider struct {
	inner core.Provider

	completeQueue chan completionJob
	streamQueue   chan streamJob
	embedQueue    chan embedJob

	done chan struct{}
	once sync.Once
}

// Wrap creates a LimitedProvider around inner with the given concurrency and
// queue sizes. workerCount is the number of goroutines (= max simultaneous
// upstream calls). queueSize is the channel buffer depth (= max waiting
// requests). Call Close() to shut down workers cleanly.
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
		if lp.streamQueue != nil {
			go lp.streamWorker()
		}
		if lp.embedQueue != nil {
			go lp.embedWorker()
		}
	}
	return lp
}

// Close signals all workers to stop after draining in-flight work.
func (lp *LimitedProvider) Close() {
	lp.once.Do(func() { close(lp.done) })
}

// --- Provider interface ---

func (lp *LimitedProvider) Name() string            { return lp.inner.Name() }
func (lp *LimitedProvider) SupportedModels() []string { return lp.inner.SupportedModels() }
func (lp *LimitedProvider) SupportsModel(m string) bool { return lp.inner.SupportsModel(m) }
func (lp *LimitedProvider) Models() []core.ModelInfo   { return lp.inner.Models() }

func (lp *LimitedProvider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
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
		// Queue full — non-blocking fast path.
		select {
		case lp.completeQueue <- job:
		case <-lp.done:
			return nil, errors.New("provider shutting down")
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	select {
	case res := <-job.response:
		return res.resp, res.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// --- StreamProvider ---

func (lp *LimitedProvider) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
	if lp.streamQueue == nil {
		return nil, errors.New("inner provider does not support streaming")
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
	}
	select {
	case res := <-job.response:
		return res.ch, res.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// --- EmbeddingProvider ---

func (lp *LimitedProvider) Embed(ctx context.Context, req core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	if lp.embedQueue == nil {
		return nil, errors.New("inner provider does not support embeddings")
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
	}
	select {
	case res := <-job.response:
		return res.resp, res.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
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
