// Package logger is the gateway's structured logging service. It wraps the
// standard library's log/slog behind a small, injectable API and emits
// structured JSON to stdout.
//
// The only knob is the level (default info), supplied from configuration
// (yaml/json) or the environment via Options and resolved once at construction.
// Callers use the Level type and its constants rather than importing log/slog
// directly, so the codebase keeps a single logging vocabulary.
package logger

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"

	"github.com/ferro-labs/ai-gateway/internal/redact"
)

// Level is the log severity. It aliases slog.Level so callers never need to
// import log/slog directly.
type Level = slog.Level

// Severity levels, re-exported so callers depend only on this package.
const (
	LevelDebug = slog.LevelDebug
	LevelInfo  = slog.LevelInfo
	LevelWarn  = slog.LevelWarn
	LevelError = slog.LevelError
)

// Logger is the gateway logging service. The underlying *slog.Logger is private
// so this package is the single place that decides how a log line is produced.
type Logger struct {
	log *slog.Logger
}

// New builds a Logger from opts. Output is structured JSON; an empty or
// unrecognized opts.Level resolves to info.
//
// Every message and string attribute passes redact.Values on the way out. This
// is the last sink in the process, and putting redaction here rather than at
// each call site is what makes it structural: a component that logs a raw
// upstream error, or a new component added later that has never heard of
// redaction, still cannot write a configured credential to stdout. Only the
// value layer runs here — the regex policies cost microseconds per line and
// stay at the error paths that call redact.String explicitly.
func New(opts Options) *Logger {
	out := opts.Output
	if out == nil {
		out = os.Stdout
	}
	handler := slog.NewJSONHandler(out, &slog.HandlerOptions{
		Level:       parseLevel(opts.Level),
		ReplaceAttr: redactAttr,
	})
	return &Logger{log: slog.New(handler)}
}

// redactAttr removes configured credential values from a log record's message
// and from every string-valued attribute, plus any attribute holding an error.
// Numbers, booleans and times cannot carry a credential literal and are
// returned untouched. The attribute is rewritten only when a credential was
// actually present, so output is byte-identical otherwise.
func redactAttr(_ []string, a slog.Attr) slog.Attr {
	switch a.Value.Kind() {
	case slog.KindString:
		s := a.Value.String()
		if r := redactLine(s); r != s {
			a.Value = slog.StringValue(r)
		}
	case slog.KindAny:
		if err, ok := a.Value.Any().(error); ok && err != nil {
			s := err.Error()
			if r := redactLine(s); r != s {
				a.Value = slog.StringValue(r)
			}
		}
	}
	return a
}

// redactLine applies the two cheap rules: exact removal of every credential the
// gateway holds, and credential-named URL query parameters, which catch a token
// written literally into a config file rather than as a ${VAR} reference. The
// regex policy set is deliberately not applied — it costs microseconds per call
// and belongs at the error sinks that invoke redact.String explicitly.
func redactLine(s string) string {
	return redact.URLCredentials(redact.Values(s))
}

// Debug logs at debug level with alternating key/value pairs.
func (l *Logger) Debug(msg string, keysAndValues ...any) { l.log.Debug(msg, keysAndValues...) }

// Info logs at info level with alternating key/value pairs.
func (l *Logger) Info(msg string, keysAndValues ...any) { l.log.Info(msg, keysAndValues...) }

// Warn logs at warn level with alternating key/value pairs.
func (l *Logger) Warn(msg string, keysAndValues ...any) { l.log.Warn(msg, keysAndValues...) }

// Error logs at error level with alternating key/value pairs.
func (l *Logger) Error(msg string, keysAndValues ...any) { l.log.Error(msg, keysAndValues...) }

// Log emits at a level chosen at runtime. Used where the level itself is
// configuration (e.g. a request-logger whose verbosity is set by config).
func (l *Logger) Log(ctx context.Context, level Level, msg string, keysAndValues ...any) {
	l.log.Log(ctx, level, msg, keysAndValues...)
}

// With returns a child logger that adds the given key/value pairs to every line.
func (l *Logger) With(keysAndValues ...any) *Logger {
	return &Logger{log: l.log.With(keysAndValues...)}
}

// WithComponent tags every line with the emitting component, e.g. "admin.keys".
func (l *Logger) WithComponent(name string) *Logger {
	return l.With("component", name)
}

// Enabled reports whether the logger emits at the given level. Use it to guard
// expensive field construction on the hot path.
func (l *Logger) Enabled(ctx context.Context, level Level) bool {
	return l.log.Enabled(ctx, level)
}

// def holds the process-wide default logger. It is never nil: init seeds it
// from the environment so logging works before bootstrap runs and in tests.
var def atomic.Pointer[Logger]

func init() { def.Store(New(FromEnv())) }

// SetDefault installs l as the process default and mirrors it into
// slog.SetDefault so any stray standard-library slog call stays consistent with
// the gateway logger. Bootstrap calls this once after building the logger.
func SetDefault(l *Logger) {
	def.Store(l)
	slog.SetDefault(l.log)
}

// Default returns the process default logger. Never nil.
func Default() *Logger { return def.Load() }

// ParseLevel converts a configuration string to a Level. It reports ok=false
// for an unrecognized value so a config loader can warn the operator; an empty
// string is valid and means the info default. New itself is lenient and falls
// back to info.
func ParseLevel(s string) (Level, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return LevelDebug, true
	case "info", "":
		return LevelInfo, true
	case "warn", "warning":
		return LevelWarn, true
	case "error":
		return LevelError, true
	default:
		return LevelInfo, false
	}
}

func parseLevel(s string) Level {
	lvl, _ := ParseLevel(s)
	return lvl
}
