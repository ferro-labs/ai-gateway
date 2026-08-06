package logger

import (
	"bufio"
	"errors"
	"net"
	"net/http"
	"time"
)

// RequestIDHeader carries the per-request trace ID on the response. The
// trace-id middleware sets it; AccessLog and panic recovery read it back off
// the response header because they may run outside the layer that owns the
// trace-id context key.
const RequestIDHeader = "X-Request-ID"

// AccessLog returns middleware that logs one structured line per HTTP request
// once it completes: method, path, status, response bytes, and duration. The
// level reflects the status class — 5xx error, 4xx warn, otherwise info — and
// the request's trace ID (from RequestIDHeader) is attached so access lines
// correlate with the rest of a request's logs.
//
// The line is emitted from a defer so a panicking handler is still logged
// (as a 500, matching the envelope the outer RecoverJSON writes). Panics are
// re-raised for RecoverJSON to handle — this middleware never swallows them.
//
// It is deliberately HTTP-level and complementary to the request-logger plugin,
// which records LLM semantics (model, tokens, cost).
func AccessLog(l *Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

			defer func() {
				panicked := recover()
				// A deliberate connection drop is not a request to log; re-raise it
				// untouched, exactly as RecoverJSON does.
				if err, ok := panicked.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(panicked)
				}
				// A hijacked connection (e.g. an upgrade proxied to /v1/realtime) is
				// a raw tunnel, not a request/response: status and byte counts are
				// meaningless, so skip the line rather than emit a misleading one.
				if rec.hijacked {
					if panicked != nil {
						panic(panicked)
					}
					return
				}
				status := rec.status
				if panicked != nil {
					// The handler unwound before writing a status; the client will
					// receive the 500 the outer RecoverJSON writes as the panic
					// propagates past this frame.
					status = http.StatusInternalServerError
				}
				lg := l
				if id := rec.Header().Get(RequestIDHeader); id != "" {
					lg = l.With("trace_id", id)
				}
				lg.Log(r.Context(), statusLevel(status), "http request",
					"method", r.Method,
					"path", r.URL.Path,
					"status", status,
					"bytes", rec.bytes,
					"duration_ms", time.Since(start).Milliseconds(),
					"remote", r.RemoteAddr,
				)
				if panicked != nil {
					panic(panicked)
				}
			}()

			next.ServeHTTP(rec, r)
		})
	}
}

// statusLevel maps an HTTP status to the level its access line is logged at.
func statusLevel(status int) Level {
	switch {
	case status >= 500:
		return LevelError
	case status >= 400:
		return LevelWarn
	default:
		return LevelInfo
	}
}

// statusRecorder captures the response status and byte count while preserving
// streaming. It implements Unwrap so http.ResponseController reaches the real
// writer's Flush/SetWriteDeadline, and Hijack so it can both delegate the
// upgrade and record that it happened (so AccessLog can skip the tunnel).
type statusRecorder struct {
	http.ResponseWriter
	status      int
	bytes       int64
	wroteHeader bool
	hijacked    bool
}

// Unwrap exposes the underlying writer to http.ResponseController.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// WriteHeader records the first final (>= 200) status, ignoring 1xx
// informational headers, then forwards to the real writer.
func (s *statusRecorder) WriteHeader(code int) {
	if !s.wroteHeader && code >= 200 {
		s.status = code
		s.wroteHeader = true
	}
	s.ResponseWriter.WriteHeader(code)
}

// Write counts the bytes written and forwards to the real writer.
func (s *statusRecorder) Write(p []byte) (int, error) {
	n, err := s.ResponseWriter.Write(p)
	s.bytes += int64(n)
	return n, err
}

// Hijack records that the connection became a raw tunnel, then delegates to the
// underlying writer through http.ResponseController (which follows Unwrap).
func (s *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	s.hijacked = true
	return http.NewResponseController(s.ResponseWriter).Hijack()
}
