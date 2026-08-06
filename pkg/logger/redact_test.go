package logger

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/internal/redact"
)

// mistralShaped has the 32-alphanumeric no-prefix format Mistral issues, which
// no shape rule can match. Built at runtime so no key-shaped literal is stored.
var mistralShaped = strings.Repeat("Ka7bQz3Rt", 3) + "Wm9XcV"

// TestLoggerRedactsConfiguredCredentials is the stdout half of F62.
//
// The leak reached stdout through log lines that never called redact at all —
// an MCP init failure logging a raw error, for one. Redacting at the logger
// rather than at each call site is what closes that class: a component that
// logs a raw upstream error cannot write a configured credential to stdout,
// whether or not its author thought about redaction.
func TestLoggerRedactsConfiguredCredentials(t *testing.T) {
	redact.RegisterSecretValues(mistralShaped)

	cases := []struct {
		name string
		emit func(*Logger)
	}{
		{
			name: "string attribute",
			emit: func(l *Logger) {
				l.Error("gateway error", "error", "mistral API error (401): invalid api_key: "+mistralShaped)
			},
		},
		{
			name: "error attribute",
			emit: func(l *Logger) {
				l.Error("mcp init failed", "error", errors.New("dial https://x/?token="+mistralShaped))
			},
		},
		{
			name: "message itself",
			emit: func(l *Logger) { l.Error("upstream rejected " + mistralShaped) },
		},
		{
			name: "attribute carried by With",
			emit: func(l *Logger) { l.With("cred", mistralShaped).Error("boom") },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			tc.emit(New(Options{Output: &buf}))
			out := buf.String()
			if strings.Contains(out, mistralShaped) {
				t.Fatalf("credential reached stdout: %s", out)
			}
			if !strings.Contains(out, "[REDACTED") {
				t.Fatalf("expected a redaction token in the line: %s", out)
			}
		})
	}
}

// TestLoggerRedactsURLCredentials covers the credential value redaction cannot
// reach: a token written literally into the config file rather than as a ${VAR}
// reference was never an environment value, so the gateway holds it only as
// config text — and it reaches stdout the moment that URL is dialled and the
// dial fails.
func TestLoggerRedactsURLCredentials(t *testing.T) {
	var buf bytes.Buffer
	New(Options{Output: &buf}).Error("mcp: server initialization failed", "server", "leaky",
		"error", errors.New(`mcp http do: Post "https://mcp.example.com/mcp?access_token=SECRET-IN-URL-12345": no such host`))

	out := buf.String()
	if strings.Contains(out, "SECRET-IN-URL-12345") {
		t.Fatalf("config-literal credential reached stdout: %s", out)
	}
	// The server stays identifiable, so the line is still diagnostic.
	if !strings.Contains(out, "mcp.example.com/mcp") {
		t.Fatalf("host should survive: %s", out)
	}
}

// TestLoggerKeepsOrdinaryQueryParameters is the counterweight: only
// credential-named parameters are redacted, so ordinary request URLs stay
// readable.
func TestLoggerKeepsOrdinaryQueryParameters(t *testing.T) {
	var buf bytes.Buffer
	New(Options{Output: &buf}).Info("upstream call", "url", "https://api.example.com/v1/models?limit=10&owner=openai")

	if out := buf.String(); !strings.Contains(out, "limit=10&owner=openai") {
		t.Fatalf("ordinary query parameters were redacted: %s", out)
	}
}

// TestLoggerLeavesOrdinaryLinesAlone pins that redaction is invisible when
// nothing matches — no rewritten values, no changed types, no extra allocation.
func TestLoggerLeavesOrdinaryLinesAlone(t *testing.T) {
	redact.RegisterSecretValues(mistralShaped)

	var buf bytes.Buffer
	New(Options{Output: &buf}).Info("gateway request",
		"model", "gpt-4o-mini", "latency_ms", 42, "stream", false)

	out := buf.String()
	for _, want := range []string{`"model":"gpt-4o-mini"`, `"latency_ms":42`, `"stream":false`} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %s in %s", want, out)
		}
	}
}

// BenchmarkLogLine measures the cost redaction adds to every emitted line
// against the same handler without it. "redacted" is the shipping path.
func BenchmarkLogLine(b *testing.B) {
	redact.RegisterSecretValues(mistralShaped)

	emit := func(b *testing.B, l *slog.Logger) {
		b.Helper()
		b.ReportAllocs()
		for b.Loop() {
			l.Info("gateway request", "trace_id", "395c0e3b543927aa84b338a11392ce9b",
				"model", "codestral-2405", "provider", "mistral", "latency_ms", 42)
		}
	}

	b.Run("plain", func(b *testing.B) {
		emit(b, slog.New(slog.NewJSONHandler(nopWriter{}, &slog.HandlerOptions{})))
	})
	b.Run("redacted", func(b *testing.B) {
		emit(b, New(Options{Output: nopWriter{}}).log)
	})
}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }
