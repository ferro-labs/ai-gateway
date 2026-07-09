package middleware

import (
	"errors"
	"net/http"
	"runtime/debug"

	"github.com/ferro-labs/ai-gateway/internal/apierror"
	"github.com/ferro-labs/ai-gateway/internal/logging"
)

// RecoverJSON recovers panics and returns the gateway's JSON error envelope.
//
// It is registered outermost so a panic in the tracing or logging middleware is
// still recovered and every inner deferred span is closed on the way out.
func RecoverJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}
			// http.ErrAbortHandler is the documented way for a handler (notably
			// httputil.ReverseProxy) to drop a connection. Let it reach net/http.
			if recoveredErr, ok := recovered.(error); ok && errors.Is(recoveredErr, http.ErrAbortHandler) {
				panic(recovered)
			}
			// Deliberately not logging.FromContext(r.Context()): this middleware
			// runs above logging.Middleware, which injects the trace ID into a
			// derived request that never reaches this closure. The header it sets
			// on the shared ResponseWriter is the only handle we have on it.
			logger := logging.Logger
			if traceID := w.Header().Get("X-Request-ID"); traceID != "" {
				logger = logger.With("trace_id", traceID)
			}
			// debug.Stack() inside the deferred function still unwinds through
			// runtime.gopanic to the frame that panicked.
			logger.Error("panic recovered", "panic", recovered, "stack", string(debug.Stack()))
			apierror.WriteOpenAI(w, http.StatusInternalServerError, "internal server error", "server_error", "internal_error")
		}()
		next.ServeHTTP(w, r)
	})
}
