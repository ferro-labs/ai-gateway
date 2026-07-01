package otel

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/ferro-labs/ai-gateway/internal/logging"
	"github.com/ferro-labs/ai-gateway/internal/redact"
	"github.com/ferro-labs/ai-gateway/observability"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// eventQueueCapacity is the size of the buffered channel used by the async
// event dispatch worker. Events that arrive when the buffer is full are
// dropped rather than blocking the caller.
const eventQueueCapacity = 1024

// otelProvider is the OTel-backed implementation of
// observability.Provider. It is constructed by Init when a real OTLP
// endpoint is configured.
type otelProvider struct {
	tracer       trace.Tracer
	privacyLevel string
	redactor     *redact.Redactor

	// mu guards exporters, eventQ, done, workerDone, and drainCtx.
	mu        sync.RWMutex
	exporters []observability.Exporter

	// Async event dispatch machinery.
	// eventQ receives events from RecordEvent; a single background worker
	// drains it and fans events out to exporters using a detached context.
	// workerDone is closed once the worker goroutine exits.
	// done signals the worker to stop accepting new events and drain.
	// drainCtx is set under mu immediately before closing done; the worker
	// reads it under mu in the drain branch so that in-flight Export calls
	// during drain honour the Shutdown deadline.
	eventQ     chan observability.Event
	done       chan struct{}
	workerDone chan struct{}
	drainCtx   context.Context // written before close(done); read by drain branch
	startOnce  sync.Once

	// dropCount tracks the total number of events dropped due to a full
	// queue. Used for sampled logging: a warning is emitted every 64 drops
	// to avoid flooding the log under sustained backpressure.
	// TODO: if per-drop flood becomes an operational issue, replace with a
	// metric counter or an exponential-backoff strategy.
	dropCount atomic.Uint64
}

// newProvider constructs an otelProvider. The tracer is obtained from
// the supplied TracerProvider.
func newProvider(tp trace.TracerProvider, cfg Config) *otelProvider {
	return &otelProvider{
		tracer:       tp.Tracer("github.com/ferro-labs/ai-gateway"),
		privacyLevel: cfg.PrivacyLevel,
		redactor:     redact.DefaultRedactor(),
	}
}

// StartRequestSpan starts the root span for an inbound gateway request.
// The returned context carries the OTel span; callers MUST propagate
// it to all downstream operations.
func (p *otelProvider) StartRequestSpan(ctx context.Context, attrs observability.RequestAttrs) (context.Context, observability.Span) {
	ctx, span := p.tracer.Start(ctx, "gateway.request", trace.WithSpanKind(trace.SpanKindServer))

	// Seed the request attribute catalog. Keep this set small and
	// stable — heavyweight attributes are added later via Set* methods.
	span.SetAttributes(
		attribute.String(observability.AttrGenAISystem, attrs.System),
		attribute.String(observability.AttrGenAIOperationName, attrs.Operation),
		attribute.String(observability.AttrGenAIRequestModel, attrs.RequestModel),
		attribute.Bool(observability.AttrGenAIRequestIsStream, attrs.IsStream),
		attribute.String(observability.AttrFerroSchemaVersion, observability.SchemaVersion),
	)
	if attrs.RoutingStrategy != "" {
		span.SetAttributes(attribute.String(observability.AttrFerroRoutingStrategy, attrs.RoutingStrategy))
	}
	if attrs.TargetKey != "" {
		span.SetAttributes(attribute.String(observability.AttrFerroRoutingTargetKey, attrs.TargetKey))
	}
	if attrs.TraceID != "" {
		span.SetAttributes(attribute.String(observability.AttrFerroGatewayTraceID, attrs.TraceID))
	}
	if attrs.ResponseModel != "" {
		span.SetAttributes(attribute.String(observability.AttrGenAIResponseModel, attrs.ResponseModel))
	}

	return ctx, &otelSpan{span: span, redactor: p.redactor, privacy: p.privacyLevel}
}

// RecordEvent enqueues an event for asynchronous delivery to every registered
// Exporter. The enqueue is non-blocking: if the internal buffer is full the
// event is dropped and a warning is logged. This keeps RecordEvent off the
// request hot-path even when exporters are slow or network-bound.
func (p *otelProvider) RecordEvent(_ context.Context, evt observability.Event) {
	p.mu.RLock()
	q := p.eventQ
	p.mu.RUnlock()

	if q == nil {
		// No worker started (no exporters attached); nothing to do.
		return
	}

	select {
	case q <- evt:
	default:
		// Buffer full — drop to avoid blocking the caller.
		// Log a sampled warning (every 64th drop) to avoid flooding the log
		// under sustained backpressure. Exporters that ignore ctx can still
		// outlive shutdown; that is on them.
		n := p.dropCount.Add(1)
		if n == 1 || n%64 == 0 {
			logging.Logger.Warn("otel: event queue full; dropping event(s)",
				"subject", evt.Subject,
				"total_dropped", n,
			)
		}
	}
}

// AttachExporters wires plugin exporters into the provider and starts the
// background dispatch worker.
//
// MUST be called exactly once at startup, after all exporter factories have
// been resolved and before the gateway begins serving traffic. A second call
// is silently ignored by startOnce, but the new exporter slice will NOT be
// picked up because the worker has already snapshotted the original slice.
// If called with an empty slice the method is a no-op (no worker started).
func (p *otelProvider) AttachExporters(exporters []observability.Exporter) {
	if len(exporters) == 0 {
		// No exporters — preserve zero-overhead-when-off property.
		return
	}

	// Start the worker exactly once. Assign p.exporters inside the Do so
	// the stored slice is always the one the worker actually uses, eliminating
	// any divergence if AttachExporters were ever called a second time.
	p.startOnce.Do(func() {
		snap := make([]observability.Exporter, len(exporters))
		copy(snap, exporters)

		q := make(chan observability.Event, eventQueueCapacity)
		done := make(chan struct{})
		workerDone := make(chan struct{})

		p.mu.Lock()
		p.exporters = snap
		p.eventQ = q
		p.done = done
		p.workerDone = workerDone
		p.mu.Unlock()

		go p.runWorker(snap, q, done, workerDone)
	})
}

// runWorker is the single background goroutine that drains eventQ and calls
// each exporter's Export. Steady-state dispatch uses context.Background() so
// events are delivered even after the originating request context has been
// cancelled. Drain-phase dispatch (after the done channel is closed) uses the
// drainCtx set by Shutdown so that Export calls are bounded by the shutdown
// deadline. Exporters that ignore ctx can still outlive shutdown; that is on
// them.
func (p *otelProvider) runWorker(
	exporters []observability.Exporter,
	q <-chan observability.Event,
	done <-chan struct{},
	workerDone chan<- struct{},
) {
	defer close(workerDone)
	for {
		select {
		case evt, ok := <-q:
			if !ok {
				// Channel closed — should not happen (we never close q), but be safe.
				return
			}
			// Steady-state: use background context so request cancellation
			// never drops an in-flight event.
			p.dispatchEvent(context.Background(), exporters, evt)
		case <-done:
			// Drain remaining buffered events before exiting. Read drainCtx
			// under the read-lock to establish a clear happens-before with the
			// Shutdown write (which sets drainCtx under the write-lock before
			// closing done).
			p.mu.RLock()
			dCtx := p.drainCtx
			p.mu.RUnlock()
			if dCtx == nil {
				dCtx = context.Background()
			}
			for {
				select {
				case evt := <-q:
					p.dispatchEvent(dCtx, exporters, evt)
				default:
					return
				}
			}
		}
	}
}

// dispatchEvent fans a single event out to all exporters using the supplied
// context. Steady-state callers pass context.Background(); the drain branch
// passes the Shutdown context so slow exporters honour the deadline.
func (p *otelProvider) dispatchEvent(ctx context.Context, exporters []observability.Exporter, evt observability.Event) {
	for _, ex := range exporters {
		_ = ex.Export(ctx, evt)
	}
}

// Shutdown stops the async worker, drains buffered events within the ctx
// deadline (best-effort), then calls each exporter's Shutdown. Safe to call
// multiple times — subsequent calls are no-ops.
//
// The shutdown context is threaded into the drain dispatch so a blocked
// exporter Export is bounded by the shutdown deadline (ctx-aware exporters
// will return; exporters that ignore ctx may still outlive shutdown).
// After Shutdown returns, RecordEvent becomes a clean no-op.
func (p *otelProvider) Shutdown(ctx context.Context) error {
	p.mu.Lock()
	done := p.done
	workerDone := p.workerDone
	exporters := p.exporters
	// Set drainCtx before closing done so the worker's drain branch reads the
	// correct context (happens-before via the mutex).
	p.drainCtx = ctx
	// Clear done and eventQ so a second Shutdown call is harmless and so
	// post-shutdown RecordEvent calls become clean no-ops (eventQ == nil
	// causes RecordEvent to return immediately without buffering).
	p.done = nil
	p.eventQ = nil
	p.mu.Unlock()

	if done != nil {
		// Signal the worker to drain and exit.
		close(done)
		// Wait for the worker to finish draining, respecting the ctx deadline.
		select {
		case <-workerDone:
		case <-ctx.Done():
			// Deadline exceeded — best-effort; proceed to exporter shutdown anyway.
		}
	}

	var firstErr error
	for _, ex := range exporters {
		if err := ex.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// otelSpan wraps an OTel span and applies the schema-defined attribute
// catalogue plus redaction policy.
type otelSpan struct {
	span     trace.Span
	redactor *redact.Redactor
	privacy  string
}

// StartChild starts a nested span under the receiver.
func (s *otelSpan) StartChild(ctx context.Context, name string, kind observability.SpanKind) (context.Context, observability.Span) {
	tr := s.span.TracerProvider().Tracer("github.com/ferro-labs/ai-gateway")
	ctx, child := tr.Start(ctx, name, trace.WithSpanKind(toOTelSpanKind(kind)))
	return ctx, &otelSpan{span: child, redactor: s.redactor, privacy: s.privacy}
}

// SetAttribute records a single attribute. The value is forwarded as
// a typed OTel attribute when possible.
func (s *otelSpan) SetAttribute(key string, value any) {
	s.span.SetAttributes(toAttribute(key, value))
}

// SetTokens stamps gen_ai.usage.* counts plus a ferro extension for
// reasoning tokens (not yet covered by upstream semconv).
func (s *otelSpan) SetTokens(input, output, reasoning int) {
	s.span.SetAttributes(
		attribute.Int(observability.AttrGenAIUsageInputTokens, input),
		attribute.Int(observability.AttrGenAIUsageOutputTokens, output),
	)
	if reasoning > 0 {
		s.span.SetAttributes(attribute.Int(observability.AttrGenAIUsageReasoningTokens, reasoning))
	}
}

// SetCost stamps the ferro.cost.* attribute set.
func (s *otelSpan) SetCost(c observability.CostBreakdown) {
	s.span.SetAttributes(
		attribute.Float64(observability.AttrFerroCostUSD, c.TotalUSD),
		attribute.Float64(observability.AttrFerroCostInputUSD, c.InputUSD),
		attribute.Float64(observability.AttrFerroCostOutputUSD, c.OutputUSD),
		attribute.Float64(observability.AttrFerroCostCacheReadUSD, c.CacheReadUSD),
		attribute.Float64(observability.AttrFerroCostCacheWriteUSD, c.CacheWriteUSD),
		attribute.Float64(observability.AttrFerroCostReasoningUSD, c.ReasoningUSD),
		attribute.Bool(observability.AttrFerroCostModelFound, c.ModelFound),
	)
}

// SetError marks the span as errored. The privacy level controls how
// much of the error message is recorded on the span:
//
//   - "none"     – no message content is leaked; status reason and the
//     recorded error both carry only the static string "redacted".
//   - "metadata" – the error message is passed through the redactor
//     before being attached (PII/secrets replaced by tokens). This is
//     the default when the privacy level is empty or unknown.
//   - "full"     – the raw error message is recorded without any
//     redaction, intended for trusted self-hosted debugging.
func (s *otelSpan) SetError(err error) {
	if err == nil {
		return
	}
	recordSpanError(s.span, s.privacy, s.redactor, err)
}

// SetStreamTimings stamps the ferro.stream.* timing attributes.
func (s *otelSpan) SetStreamTimings(ttftMs, ttltMs float64) {
	s.span.SetAttributes(
		attribute.Float64(observability.AttrFerroStreamTimeToFirstTokenMs, ttftMs),
		attribute.Float64(observability.AttrFerroStreamTimeToLastTokenMs, ttltMs),
	)
}

// End ends the underlying OTel span.
func (s *otelSpan) End() { s.span.End() }

// redactedError is a minimal error wrapper used by SetError so the
// OTel SDK records the redacted message in span.recordError.
type redactedError string

func (e redactedError) Error() string { return string(e) }

// toOTelSpanKind maps observability.SpanKind to trace.SpanKind.
func toOTelSpanKind(k observability.SpanKind) trace.SpanKind {
	switch k {
	case observability.SpanKindServer:
		return trace.SpanKindServer
	case observability.SpanKindClient:
		return trace.SpanKindClient
	default:
		return trace.SpanKindInternal
	}
}

// toAttribute converts an arbitrary Go value to an OTel attribute.
// Unsupported types fall back to fmt.Sprintf via attribute.String.
func toAttribute(key string, value any) attribute.KeyValue {
	switch v := value.(type) {
	case string:
		return attribute.String(key, v)
	case bool:
		return attribute.Bool(key, v)
	case int:
		return attribute.Int(key, v)
	case int64:
		return attribute.Int64(key, v)
	case float64:
		return attribute.Float64(key, v)
	case []string:
		return attribute.StringSlice(key, v)
	default:
		return attribute.String(key, sprint(v))
	}
}

// RecordingEnabled returns true when at least one plugin Exporter is
// attached to this provider. The gateway caches this value at startup
// so the hot path can skip Event construction when no exporter is
// listening.
func (p *otelProvider) RecordingEnabled() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.exporters) > 0
}

// Compile-time interface guards.
var (
	_ observability.Provider               = (*otelProvider)(nil)
	_ observability.EventRecordingProvider = (*otelProvider)(nil)
	_ observability.Span                   = (*otelSpan)(nil)
)
