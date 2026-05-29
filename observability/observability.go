package observability

import "context"

// Provider is the single seam between the gateway core and any
// observability backend. internal/otel returns a real implementation;
// NoOp() returns a zero-allocation default.
//
// The gateway holds exactly one Provider for its lifetime, supplied
// via Config at startup.
type Provider interface {
	// StartRequestSpan opens the root span for an incoming gateway
	// request. The returned context carries the active span and MUST be
	// used for all child operations. End() on the returned Span finalises
	// the span and flushes it to the configured exporters.
	StartRequestSpan(ctx context.Context, attrs RequestAttrs) (context.Context, Span)

	// RecordEvent broadcasts a non-span event (e.g. a completed-request
	// hook payload) to every registered Exporter. This is the bridge
	// between the existing internal/events.HookEvent fanout and the
	// plugin Exporter ecosystem.
	RecordEvent(ctx context.Context, evt Event)

	// Shutdown drains all in-flight exports within the deadline on ctx.
	// Returns the first error encountered, but never blocks longer than
	// the supplied deadline.
	Shutdown(ctx context.Context) error
}

// Span is the per-request handle returned by StartRequestSpan and
// StartChild. Closing it with End() ends the span and propagates
// timings to the active exporters.
type Span interface {
	// StartChild opens a nested span under this one. The returned
	// context carries the child span.
	StartChild(ctx context.Context, name string, kind SpanKind) (context.Context, Span)

	// SetAttribute records a single key/value attribute on the span.
	// Keys SHOULD come from the AttrXxx constants in attributes.go.
	SetAttribute(key string, value any)

	// SetTokens records gen_ai.usage.* token counts on the span.
	SetTokens(input, output, reasoning int)

	// SetCost records ferro.cost.* attributes on the span.
	SetCost(c CostBreakdown)

	// SetError marks the span as failed and records the error message.
	// Implementations apply the redaction policy from internal/redact
	// before persisting the message.
	SetError(err error)

	// SetStreamTimings records ferro.stream.time_to_first_token_ms and
	// ferro.stream.time_to_last_token_ms. Call only for streaming
	// requests.
	SetStreamTimings(ttftMs, ttltMs float64)

	// End finalises the span. After End the Span MUST NOT be reused.
	End()
}

// SpanKind mirrors the OpenTelemetry SpanKind enum. The OTel SDK
// mapping lives in internal/otel.
type SpanKind uint8

// SpanKind values.
const (
	SpanKindInternal SpanKind = iota // INTERNAL (default for in-process work)
	SpanKindServer                   // SERVER  (inbound request handler)
	SpanKindClient                   // CLIENT  (outbound provider call)
)

// EventRecordingProvider is an optional interface that Providers may
// implement to signal whether any exporter is currently listening.  The
// gateway checks this at startup (via a type assertion on the value
// returned by internal/otel.Init) to set a cached "events active" flag,
// allowing the hot path to skip Event construction entirely when no
// exporter is registered — preserving the zero-allocation guarantee of
// the NoOp path.
//
// NoOp does NOT implement this interface.  The gateway interprets the
// absence of the interface as "no events active".
type EventRecordingProvider interface {
	Provider
	// RecordingEnabled returns true when at least one Exporter is
	// attached and will receive RecordEvent calls.
	RecordingEnabled() bool
}

// Exporter is implemented by every observability plugin in the
// ai-gateway-plugins repository (langsmith, langfuse, phoenix,
// datadog, newrelic, sentry, helicone, honeycomb, grafana, …).
//
// Plugins register themselves via init() → RegisterExporter so the
// gateway can discover them when assembled via ferrogw-builder.
type Exporter interface {
	// Name returns the canonical name of the exporter, e.g. "langsmith".
	// Used to look up exporter-specific configuration.
	Name() string

	// Init is called once at startup with a deadline-bounded context and the
	// exporter's configuration block from gateway config. Exporters that
	// authenticate or open connections SHOULD honour the context deadline.
	Init(ctx context.Context, cfg map[string]any) error

	// Export delivers a single Event to the backing system. Implementations
	// MUST be safe for concurrent use.
	Export(ctx context.Context, evt Event) error

	// Shutdown drains the exporter's buffers within the supplied
	// deadline. Called once at gateway shutdown.
	Shutdown(ctx context.Context) error
}
