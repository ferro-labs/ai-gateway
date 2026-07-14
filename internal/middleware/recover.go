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
		cw := &committedWriter{ResponseWriter: w}
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
			if traceID := cw.Header().Get(logging.RequestIDHeader); traceID != "" {
				logger = logger.With("trace_id", traceID)
			}
			// debug.Stack() inside the deferred function still unwinds through
			// runtime.gopanic to the frame that panicked.
			logger.Error("panic recovered",
				"panic", recovered,
				"response_committed", cw.committed,
				"stack", string(debug.Stack()),
			)
			// Once the status line or any body byte has gone out — a streamed
			// response, say — an error envelope would append garbage to what the
			// client already has. Log it and let the truncated response stand.
			if !cw.committed {
				apierror.WriteOpenAI(w, http.StatusInternalServerError, "internal server error", "server_error", "internal_error")
			}
		}()
		next.ServeHTTP(cw, r)
	})
}

// committedWriter records whether any response bytes have reached the client.
//
// It implements Unwrap so http.NewResponseController still reaches the real
// writer's Flush, Hijack, and SetWriteDeadline; no gateway code type-asserts a
// request ResponseWriter to http.Flusher or http.Hijacker directly.
type committedWriter struct {
	http.ResponseWriter
	committed bool
}

func (w *committedWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// WriteHeader commits only on a final status. net/http allows any number of 1xx
// informational headers before the single 2xx-5xx one, and httputil.ReverseProxy
// forwards an upstream 1xx (e.g. 103 Early Hints) straight through — treating
// that as committed would leave a panicking request with no response at all.
func (w *committedWriter) WriteHeader(status int) {
	if status >= 200 {
		w.committed = true
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *committedWriter) Write(p []byte) (int, error) {
	w.committed = true
	return w.ResponseWriter.Write(p)
}
