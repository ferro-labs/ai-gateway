package otel

import (
	"net/http"

	"github.com/ferro-labs/ai-gateway/observability"
	"github.com/ferro-labs/ai-gateway/pkg/logger"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// Middleware extracts a W3C traceparent + tracestate + baggage from inbound
// HTTP requests and seeds the context with an OTel span context, a logging
// trace ID, and the request identity.
//
// It MUST be mounted BEFORE the trace-id middleware (logger.Middleware) in
// the chi router stack so the logging layer can reuse the OTel-derived trace
// ID via logger.WithTraceID. This unification protocol keeps OTel trace_id,
// logger.TraceIDFromContext, X-Request-ID, and ferro.gateway.trace_id all
// equal per request.
//
// When no inbound traceparent is present the middleware leaves the trace ID
// alone (logger.Middleware generates one downstream). When the inbound
// traceparent is valid we copy the 16-byte trace_id into the logging context
// so all four representations agree.
//
// The request identity — X-User-ID, X-Session-ID, and the baggage entries
// user.id and session.id — is placed on the context with
// observability.ContextWithRequestIdentity only when at least one is present,
// so an anonymous request allocates nothing here.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := otel.GetTextMapPropagator().Extract(
			r.Context(),
			propagation.HeaderCarrier(r.Header),
		)
		if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
			ctx = logger.WithTraceID(ctx, sc.TraceID().String())
		}
		if id := requestIdentityFromHeaders(ctx, r.Header); !id.IsZero() {
			ctx = observability.ContextWithRequestIdentity(ctx, id)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
