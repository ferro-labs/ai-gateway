// Package logging provides structured JSON logging with trace ID propagation.
// It wraps Go's built-in log/slog with gateway-specific helpers: a per-request
// trace ID injected via middleware and extracted from context.
package logging

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"os"
)

type contextKey string

const traceIDKey contextKey = "trace_id"

// Logger is the package-level structured logger. Callers should prefer
// FromContext(ctx) to automatically attach the request trace ID.
var Logger *slog.Logger

func init() {
	Setup(os.Getenv("LOG_LEVEL"), os.Getenv("LOG_FORMAT"))
}

// Setup (re-)initialises the package logger. level is one of debug/info/warn/error
// (default info). format is "json" (default) or "text".
func Setup(level, format string) {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: lvl}
	var handler slog.Handler
	if format == "text" {
		handler = slog.NewTextHandler(os.Stdout, opts)
	} else {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}
	Logger = slog.New(handler)
	slog.SetDefault(Logger)
}

// NewTraceID generates a random 16-byte hex trace ID.
func NewTraceID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// WithTraceID stores a trace ID in the context.
func WithTraceID(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, traceIDKey, traceID)
}

// TraceIDFromContext retrieves the trace ID stored in the context.
func TraceIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(traceIDKey).(string)
	return v
}

// FromContext returns a *slog.Logger pre-annotated with the trace_id from ctx.
func FromContext(ctx context.Context) *slog.Logger {
	if id := TraceIDFromContext(ctx); id != "" {
		return Logger.With("trace_id", id)
	}
	return Logger
}

// Middleware injects a trace ID into every request context and echoes it in
// the X-Request-ID response header. Uses the incoming X-Request-ID header if
// present, otherwise generates a new one.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceID := r.Header.Get("X-Request-ID")
		if traceID == "" {
			traceID = NewTraceID()
		}
		ctx := WithTraceID(r.Context(), traceID)
		w.Header().Set("X-Request-ID", traceID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
