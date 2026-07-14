package otel

import (
	"sync/atomic"

	"github.com/ferro-labs/ai-gateway/internal/redact"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// spanErrorPolicy captures the privacy level and redactor RecordSpanError
// applies to raw spans. Both fields are read together, so they are published
// atomically as a single immutable value.
type spanErrorPolicy struct {
	level    string
	redactor *redact.Redactor
}

// spanErrorPolicyPtr holds the active policy. It defaults to metadata
// redaction so child spans created before (or without) Init still redact
// error text instead of leaking raw messages.
var spanErrorPolicyPtr atomic.Pointer[spanErrorPolicy]

func init() {
	spanErrorPolicyPtr.Store(&spanErrorPolicy{
		level:    PrivacyLevelMetadata,
		redactor: redact.DefaultRedactor(),
	})
}

// setSpanErrorPolicy publishes the privacy level and redactor RecordSpanError
// uses. Init calls it once the provider is built; a nil redactor falls back to
// the default so redaction is never disabled by accident.
func setSpanErrorPolicy(level string, redactor *redact.Redactor) {
	if redactor == nil {
		redactor = redact.DefaultRedactor()
	}
	spanErrorPolicyPtr.Store(&spanErrorPolicy{level: level, redactor: redactor})
}

// RecordSpanError records err on a raw OTel span using the configured privacy
// policy, so child spans created from the global tracer (plugin stages, MCP
// tool calls) honour privacy_level instead of leaking raw error text. It is a
// no-op when err is nil.
func RecordSpanError(span trace.Span, err error) {
	if err == nil {
		return
	}
	p := spanErrorPolicyPtr.Load()
	recordSpanError(span, p.level, p.redactor, err)
}

// recordSpanError applies the privacy-scoped error-recording logic shared by
// otelSpan.SetError and RecordSpanError. err MUST be non-nil.
func recordSpanError(span trace.Span, privacy string, redactor *redact.Redactor, err error) {
	switch privacy {
	case PrivacyLevelNone:
		// Do not leak any message content — record only the static string
		// "redacted". AddEvent (not RecordError) is used so exception.type is
		// fully controlled: RecordError always appends the concrete Go error
		// type last, which would expose an internal type name and violate the
		// none level's maximum-opacity intent.
		span.SetStatus(codes.Error, "redacted")
		span.AddEvent(semconv.ExceptionEventName, trace.WithAttributes(
			semconv.ExceptionTypeKey.String("error"),
			semconv.ExceptionMessageKey.String("redacted"),
		))
	case PrivacyLevelFull:
		// Attach the raw error message with no redaction.
		raw := err.Error()
		span.SetStatus(codes.Error, raw)
		span.RecordError(redactedError(raw))
	default:
		// "metadata" and any unknown/empty value: apply redaction (safe default).
		msg := redactor.Redact(err.Error())
		span.SetStatus(codes.Error, msg)
		span.RecordError(redactedError(msg))
	}
}
