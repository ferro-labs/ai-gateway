package aigateway

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/events"
	"github.com/ferro-labs/ai-gateway/internal/logging"
	"github.com/ferro-labs/ai-gateway/internal/metrics"
	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/observability"
)

// maxHookWorkers caps the size of the shared async hook-dispatch worker pool.
// The pool is sized to GOMAXPROCS but never exceeds this so a high-core host
// does not spawn an unbounded number of hook workers.
const maxHookWorkers = 4

// EventHookFunc is called asynchronously after a gateway event (request
// completed or failed). It replaces the old EventPublisher interface with a
// simpler function-based hook pattern.
type EventHookFunc func(ctx context.Context, subject string, data map[string]any)

// hookDispatch is a work item handed to the async hook workers over a channel.
// Storing ctx in the struct is the documented exception to "don't store a context
// in a struct": the context travels *with* the work item to the goroutine that
// processes it, rather than outliving a call. See the Go blog's guidance on
// passing request-scoped values through a pipeline.
type hookDispatch struct {
	ctx   context.Context
	event events.HookEvent
	hook  EventHookFunc
}

// hookBus owns the asynchronous event-hook subsystem: the registered hooks,
// their lock-free snapshot, the bounded dispatch queue, and the worker pool. Its
// own mutex serializes hook registration; every read path goes through the
// atomic snapshot, so the bus holds no locking relationship with the rest of the
// Gateway and can guard its state independently of g.mu.
type hookBus struct {
	mu          sync.Mutex
	hooks       []EventHookFunc
	snapshot    atomic.Value // []EventHookFunc
	dispatchQ   chan hookDispatch
	workersDone sync.WaitGroup
}

// newHookBus returns a hookBus with an empty snapshot and a dispatch queue of
// the given capacity. Workers are started separately via start.
func newHookBus(queueSize int) *hookBus {
	b := &hookBus{dispatchQ: make(chan hookDispatch, queueSize)}
	b.snapshot.Store([]EventHookFunc{})
	return b
}

// add registers a hook and republishes the snapshot read by the hot path.
func (b *hookBus) add(fn EventHookFunc) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.hooks = append(b.hooks, fn)
	b.snapshot.Store(append([]EventHookFunc(nil), b.hooks...))
}

func (b *hookBus) current() []EventHookFunc {
	hooks, _ := b.snapshot.Load().([]EventHookFunc)
	return hooks
}

func (b *hookBus) hasHooks() bool {
	return len(b.current()) > 0
}

// publish enqueues event to every registered hook. shutdownCtx governs the
// drop-vs-block decision on a shutting-down bus; a nil shutdownCtx keeps the
// pre-New literal-Gateway behavior used by a handful of unit tests.
func (b *hookBus) publish(ctx, shutdownCtx context.Context, event events.HookEvent) {
	hooks := b.current()
	if len(hooks) == 0 {
		return
	}

	// Detach from the request lifecycle: hooks are dispatched asynchronously
	// and usually run after the HTTP handler has returned and ctx is already
	// cancelled. WithoutCancel drops cancellation (so ctx-aware hook work like
	// DB writes / outbound calls is not dead-on-arrival) while preserving the
	// request's trace context and values. Worker shutdown is governed by
	// shutdownCtx, not this context.
	detachedCtx := context.WithoutCancel(ctx)

	for _, hook := range hooks {
		dispatch := hookDispatch{
			ctx:   detachedCtx,
			event: event,
			hook:  hook,
		}

		// Bias toward the shutdown check first so we never race a Close()
		// that has already cancelled. Once shutdownCtx is Done we drop the
		// event rather than risk a send on what used to be a closed channel
		// (we no longer close dispatchQ — workers exit via shutdownCtx).
		// The nil-shutdownCtx branch supports a handful of unit tests that
		// build Gateway literals directly without going through New().
		if shutdownCtx != nil {
			select {
			case <-shutdownCtx.Done():
				return
			default:
			}
			select {
			case b.dispatchQ <- dispatch:
			case <-shutdownCtx.Done():
				return
			default:
				metrics.HookEventsDroppedTotal.WithLabelValues(event.Subject).Inc()
			}
			continue
		}
		select {
		case b.dispatchQ <- dispatch:
		default:
			// Queue full — drop hook dispatches to avoid unbounded goroutine creation.
			metrics.HookEventsDroppedTotal.WithLabelValues(event.Subject).Inc()
		}
	}
}

// start spawns the worker pool. shutdownCtx signals workers to drain and exit.
func (b *hookBus) start(shutdownCtx context.Context) {
	workerCount := runtime.GOMAXPROCS(0)
	if workerCount < 1 {
		workerCount = 1
	}
	if workerCount > maxHookWorkers {
		workerCount = maxHookWorkers
	}

	for range workerCount {
		b.workersDone.Add(1)
		go func() {
			defer b.workersDone.Done()
			for {
				select {
				case <-shutdownCtx.Done():
					// Best-effort drain anything queued before exiting so we
					// don't lose events that were already enqueued.
					for {
						select {
						case dispatch := <-b.dispatchQ:
							runHookDispatch(dispatch)
						default:
							return
						}
					}
				case dispatch := <-b.dispatchQ:
					runHookDispatch(dispatch)
				}
			}
		}()
	}
}

// wait blocks until every hook worker has exited.
func (b *hookBus) wait() {
	b.workersDone.Wait()
}

// AddHook registers an EventHookFunc that is called asynchronously on each
// completed or failed request. Multiple hooks may be registered; all are
// invoked for every event on the shared bounded hook worker pool, so hook
// implementations should return promptly and avoid indefinite blocking.
func (g *Gateway) AddHook(fn EventHookFunc) {
	g.hooks.add(fn)
}

func (g *Gateway) hasHooks() bool {
	return g.hooks.hasHooks()
}

// publishEvent calls all registered hooks asynchronously.
func (g *Gateway) publishEvent(ctx context.Context, event events.HookEvent) {
	g.hooks.publish(ctx, g.shutdownCtx, event)
}

func runHookDispatch(dispatch hookDispatch) {
	data := dispatch.event.Map()
	defer func() {
		if r := recover(); r != nil {
			logging.Logger.Error("event hook panicked",
				"subject", dispatch.event.Subject,
				"panic", r,
			)
		}
	}()
	dispatch.hook(dispatch.ctx, dispatch.event.Subject, data)
}

func failedEventData(traceID, provider, model, errMsg string, latency time.Duration, stream bool) events.HookEvent {
	return events.FailedRequest(traceID, provider, model, errMsg, latency, stream)
}

func completedEventData(traceID, provider, model string, latency time.Duration, stream bool, tokensIn, tokensOut int, cost models.CostResult) events.HookEvent {
	return events.CompletedRequest(traceID, provider, model, latency, stream, tokensIn, tokensOut, cost, true)
}

// obsEventFromHook converts an internal HookEvent into the public
// observability.Event that is broadcast to plugin Exporters via
// Provider.RecordEvent. No prompt or response content is included —
// only request metadata and usage/cost numbers.
func obsEventFromHook(e events.HookEvent) observability.Event {
	return observability.Event{
		Subject:   e.Subject,
		TraceID:   e.TraceID,
		Provider:  e.Provider,
		Model:     e.Model,
		Status:    e.Status,
		Error:     e.Error,
		LatencyMs: e.LatencyMs,
		Stream:    e.Stream,
		TokensIn:  e.TokensIn,
		TokensOut: e.TokensOut,
		Cost: observability.CostBreakdown{
			TotalUSD:      e.Cost.TotalUSD,
			InputUSD:      e.Cost.InputUSD,
			OutputUSD:     e.Cost.OutputUSD,
			CacheReadUSD:  e.Cost.CacheReadUSD,
			CacheWriteUSD: e.Cost.CacheWriteUSD,
			ReasoningUSD:  e.Cost.ReasoningUSD,
			ModelFound:    e.Cost.ModelFound,
		},
		Timestamp: e.Timestamp,
	}
}
