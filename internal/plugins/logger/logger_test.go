package logger

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/internal/logging"
	"github.com/ferro-labs/ai-gateway/internal/requestlog"
	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
)

// recordingWriter captures written requestlog.Entry values for inspection.
type recordingWriter struct {
	entries []requestlog.Entry
}

func (w *recordingWriter) Write(_ context.Context, entry requestlog.Entry) error {
	w.entries = append(w.entries, entry)
	return nil
}

func TestRequestLogger_Init(t *testing.T) {
	t.Run("default level", func(t *testing.T) {
		l := &RequestLogger{}
		if err := l.Init(map[string]any{}); err != nil {
			t.Fatalf("Init failed: %v", err)
		}
		if l.logLevel != slog.LevelInfo {
			t.Errorf("expected default level Info, got %v", l.logLevel)
		}
	})

	t.Run("debug level", func(t *testing.T) {
		l := &RequestLogger{}
		if err := l.Init(map[string]any{"level": "debug"}); err != nil {
			t.Fatalf("Init failed: %v", err)
		}
		if l.logLevel != slog.LevelDebug {
			t.Errorf("expected Debug level, got %v", l.logLevel)
		}
	})
}

func TestRequestLogger_ExecuteRequest(t *testing.T) {
	l := &RequestLogger{}
	if err := l.Init(map[string]any{}); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	req := &providers.Request{
		Model: "gpt-4",
		Messages: []providers.Message{
			{Role: "user", Content: "hello"},
		},
	}
	pctx := plugin.NewContext(req)

	if err := l.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
}

func TestRequestLogger_ExecuteResponse(t *testing.T) {
	l := &RequestLogger{}
	if err := l.Init(map[string]any{}); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	req := &providers.Request{
		Model: "gpt-4",
		Messages: []providers.Message{
			{Role: "user", Content: "hello"},
		},
	}
	pctx := plugin.NewContext(req)
	pctx.Response = &providers.Response{
		Model:    "gpt-4",
		Provider: "openai",
		Usage:    providers.Usage{PromptTokens: 5, CompletionTokens: 10, TotalTokens: 15},
		Choices: []providers.Choice{
			{Index: 0, Message: providers.Message{Role: "assistant", Content: "hi"}, FinishReason: "stop"},
		},
	}

	if err := l.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
}

func TestRequestLogger_ExecuteError(t *testing.T) {
	l := &RequestLogger{}
	if err := l.Init(map[string]any{}); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	req := &providers.Request{
		Model: "gpt-4",
		Messages: []providers.Message{
			{Role: "user", Content: "hello"},
		},
	}
	pctx := plugin.NewContext(req)
	pctx.Error = errors.New("provider timeout")

	if err := l.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
}

func TestRequestLogger_ExecuteErrorWithoutRequest(t *testing.T) {
	l := &RequestLogger{}
	if err := l.Init(map[string]any{}); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	pctx := plugin.NewContext(nil)
	pctx.Error = errors.New("provider timeout")

	if err := l.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
}

func TestRequestLogger_Name(t *testing.T) {
	l := &RequestLogger{}
	if l.Name() != "request-logger" {
		t.Errorf("Name() = %q, want %q", l.Name(), "request-logger")
	}
}

func TestRequestLogger_Type(t *testing.T) {
	l := &RequestLogger{}
	if l.Type() != plugin.TypeLogging {
		t.Errorf("Type() = %v, want TypeLogging", l.Type())
	}
}

func TestRequestLogger_Init_WarnLevel(t *testing.T) {
	l := &RequestLogger{}
	if err := l.Init(map[string]any{"level": "warn"}); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if l.logLevel != slog.LevelWarn {
		t.Errorf("expected Warn level, got %v", l.logLevel)
	}
}

func TestRequestLogger_Init_ErrorLevel(t *testing.T) {
	l := &RequestLogger{}
	if err := l.Init(map[string]any{"level": "error"}); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if l.logLevel != slog.LevelError {
		t.Errorf("expected Error level, got %v", l.logLevel)
	}
}

func TestRequestLogger_Init_UnsupportedBackend(t *testing.T) {
	l := &RequestLogger{}
	err := l.Init(map[string]any{
		"persist": true,
		"backend": "cassandra",
		"dsn":     "",
	})
	if err == nil {
		t.Error("expected error for unsupported backend")
	}
}

func TestRequestLogger_ExecuteErrorRedactsKeyInLog(t *testing.T) {
	// Replace the package-level logger with one that captures output to a buffer,
	// so we can verify the logged error message is redacted.
	oldLogger := logging.Logger
	defer func() { logging.Logger = oldLogger }()

	var buf bytes.Buffer
	logging.Logger = slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	l := &RequestLogger{}
	if err := l.Init(map[string]any{}); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Build a fake OpenAI-style key at runtime to avoid credential-scanner false positives.
	fakeKey := "sk-" + strings.Repeat("x", 40)
	pctx := plugin.NewContext(nil)
	pctx.Error = errors.New("upstream rejected: " + fakeKey)

	if err := l.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	logged := buf.String()
	if strings.Contains(logged, fakeKey) {
		t.Errorf("key was not redacted in log output; found in: %q", logged)
	}
	if !strings.Contains(logged, "[REDACTED") {
		t.Errorf("expected REDACTED marker in log output; got: %q", logged)
	}
}

// TestRequestLogger_ExecuteErrorRedactsKeyInEntry verifies that the
// requestlog.Entry written by the on_error path has its ErrorMessage field
// redacted, not just the structured log line. A recording writer is used so
// the assertion is made against the persisted Entry directly.
func TestRequestLogger_ExecuteErrorRedactsKeyInEntry(t *testing.T) {
	l := &RequestLogger{}
	if err := l.Init(map[string]any{}); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Swap in the recording writer before Execute is called.
	rec := &recordingWriter{}
	l.writer = rec

	// Build a fake OpenAI-style key at runtime to avoid credential-scanner false positives.
	fakeKey := "sk-" + strings.Repeat("y", 40)
	pctx := plugin.NewContext(nil)
	pctx.Error = errors.New("upstream rejected: " + fakeKey)

	if err := l.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if len(rec.entries) == 0 {
		t.Fatal("expected at least one requestlog.Entry to be written")
	}
	entry := rec.entries[0]

	// The persisted ErrorMessage must not contain the raw key.
	if strings.Contains(entry.ErrorMessage, fakeKey) {
		t.Errorf("ErrorMessage contains raw key; got %q", entry.ErrorMessage)
	}
	// It must carry the redaction marker instead.
	if !strings.Contains(entry.ErrorMessage, "[REDACTED") {
		t.Errorf("ErrorMessage missing REDACTED marker; got %q", entry.ErrorMessage)
	}
}
