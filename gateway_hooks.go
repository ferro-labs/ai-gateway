package aigateway

import (
	"context"
	"runtime"
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

// AddHook registers an EventHookFunc that is called asynchronously on each
// completed or failed request. Multiple hooks may be registered; all are
// invoked for every event on the shared bounded hook worker pool, so hook
// implementations should return promptly and avoid indefinite blocking.
func (g *Gateway) AddHook(fn EventHookFunc) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.hooks = append(g.hooks, fn)
	g.hookSnapshot.Store(append([]EventHookFunc(nil), g.hooks...))
}

func (g *Gateway) hasHooks() bool {
	return len(g.currentHooks()) > 0
}

// publishEvent calls all registered hooks asynchronously.
func (g *Gateway) publishEvent(ctx context.Context, event events.HookEvent) {
	hooks := g.currentHooks()
	if len(hooks) == 0 {
		return
	}

	// Detach from the request lifecycle: hooks are dispatched asynchronously
	// and usually run after the HTTP handler has returned and ctx is already
	// cancelled. WithoutCancel drops cancellation (so ctx-aware hook work like
	// DB writes / outbound calls is not dead-on-arrival) while preserving the
	// request's trace context and values. Worker shutdown is governed by
	// g.shutdownCtx, not this context.
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
		// (we no longer close hookDispatchQ — workers exit via shutdownCtx).
		// The nil-shutdownCtx branch supports a handful of unit tests that
		// build Gateway literals directly without going through New().
		if g.shutdownCtx != nil {
			select {
			case <-g.shutdownCtx.Done():
				return
			default:
			}
			select {
			case g.hookDispatchQ <- dispatch:
			case <-g.shutdownCtx.Done():
				return
			default:
				metrics.HookEventsDroppedTotal.WithLabelValues(event.Subject).Inc()
			}
			continue
		}
		select {
		case g.hookDispatchQ <- dispatch:
		default:
			// Queue full — drop hook dispatches to avoid unbounded goroutine creation.
			metrics.HookEventsDroppedTotal.WithLabelValues(event.Subject).Inc()
		}
	}
}

func (g *Gateway) currentHooks() []EventHookFunc {
	hooks, _ := g.hookSnapshot.Load().([]EventHookFunc)
	return hooks
}

func (g *Gateway) startHookWorkers() {
	workerCount := runtime.GOMAXPROCS(0)
	if workerCount < 1 {
		workerCount = 1
	}
	if workerCount > maxHookWorkers {
		workerCount = maxHookWorkers
	}

	for range workerCount {
		g.hookWorkersDone.Add(1)
		go func() {
			defer g.hookWorkersDone.Done()
			for {
				select {
				case <-g.shutdownCtx.Done():
					// Best-effort drain anything queued before exiting so we
					// don't lose events that were already enqueued.
					for {
						select {
						case dispatch := <-g.hookDispatchQ:
							runHookDispatch(dispatch)
						default:
							return
						}
					}
				case dispatch := <-g.hookDispatchQ:
					runHookDispatch(dispatch)
				}
			}
		}()
	}
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
