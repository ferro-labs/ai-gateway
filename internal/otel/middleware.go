package otel

import (
	"net/http"

	"github.com/ferro-labs/ai-gateway/internal/logging"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// Middleware extracts a W3C traceparent + tracestate + baggage from
// inbound HTTP requests and seeds the context with both an OTel span
// context and a logging trace ID.
//
// It MUST be mounted BEFORE internal/logging.Middleware in the chi
// router stack so the logging layer can reuse the OTel-derived trace
// ID via logging.WithTraceID. This unification protocol keeps OTel
// trace_id, logging.TraceIDFromContext, X-Request-ID, and
// ferro.gateway.trace_id all equal per request.
//
// When no inbound traceparent is present the middleware does nothing
// (logging.Middleware will generate a request ID downstream). When the
// inbound traceparent is valid we copy the 16-byte trace_id into the
// logging context so all four representations agree.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := otel.GetTextMapPropagator().Extract(
			r.Context(),
			propagation.HeaderCarrier(r.Header),
		)
		if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
			ctx = logging.WithTraceID(ctx, sc.TraceID().String())
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
