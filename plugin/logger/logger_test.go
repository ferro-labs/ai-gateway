package logger

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/internal/requestlog"
	"github.com/ferro-labs/ai-gateway/pkg/logger"
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
		if l.logLevel != logger.LevelInfo {
			t.Errorf("expected default level Info, got %v", l.logLevel)
		}
	})

	t.Run("debug level", func(t *testing.T) {
		l := &RequestLogger{}
		if err := l.Init(map[string]any{"level": "debug"}); err != nil {
			t.Fatalf("Init failed: %v", err)
		}
		if l.logLevel != logger.LevelDebug {
			t.Errorf("expected Debug level, got %v", l.logLevel)
		}
	})
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
	pctx.Stage = plugin.StageAfterRequest
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
	if l.logLevel != logger.LevelWarn {
		t.Errorf("expected Warn level, got %v", l.logLevel)
	}
}

func TestRequestLogger_Init_ErrorLevel(t *testing.T) {
	l := &RequestLogger{}
	if err := l.Init(map[string]any{"level": "error"}); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if l.logLevel != logger.LevelError {
		t.Errorf("expected Error level, got %v", l.logLevel)
	}
}

// The plugin no longer opens a store from config, so obsolete backend/dsn
// options are ignored rather than validated. Persistence targets the shared
// store the gateway injects.
func TestRequestLogger_Init_IgnoresObsoleteStorageOptions(t *testing.T) {
	l := &RequestLogger{}
	if err := l.Init(map[string]any{
		"persist": true,
		"backend": "cassandra", // once an error; now ignored
		"dsn":     "/etc/passwd",
	}); err != nil {
		t.Fatalf("Init must not fail on obsolete storage options: %v", err)
	}
	// With no injected store, persistence falls back to the no-op writer; a
	// request-supplied dsn is never opened.
	if _, ok := l.writer.(requestlog.NoopWriter); !ok {
		t.Fatalf("writer = %T, want NoopWriter when no store is injected", l.writer)
	}
}

// persist:true directs writes at the injected shared store.
func TestRequestLogger_Init_PersistsToInjectedWriter(t *testing.T) {
	rec := &recordingWriter{}
	l := &RequestLogger{}
	l.SetRequestLogWriter(rec)
	if err := l.Init(map[string]any{"persist": true}); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if l.writer != requestlog.Writer(rec) {
		t.Fatalf("writer = %T, want the injected recordingWriter", l.writer)
	}

	pctx := plugin.NewContext(&providers.Request{Model: "gpt-4"})
	pctx.Stage = plugin.StageBeforeRequest
	if err := l.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if len(rec.entries) == 0 {
		t.Fatal("a persisted request produced no entry in the injected store")
	}
}

// persist:false records nothing even when a store is injected — the operator
// wants stdout logging only.
func TestRequestLogger_Init_PersistFalseDoesNotWrite(t *testing.T) {
	rec := &recordingWriter{}
	l := &RequestLogger{}
	l.SetRequestLogWriter(rec)
	if err := l.Init(map[string]any{"persist": false}); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if _, ok := l.writer.(requestlog.NoopWriter); !ok {
		t.Fatalf("writer = %T, want NoopWriter when persist is false", l.writer)
	}

	pctx := plugin.NewContext(&providers.Request{Model: "gpt-4"})
	pctx.Stage = plugin.StageBeforeRequest
	if err := l.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if len(rec.entries) != 0 {
		t.Fatalf("persist:false wrote %d entries to the store", len(rec.entries))
	}
}

// Close must not close the shared store; the gateway owns it, and the admin log
// reader keeps using it after a plugin reload.
func TestRequestLogger_Close_DoesNotCloseSharedStore(t *testing.T) {
	closed := false
	l := &RequestLogger{}
	l.SetRequestLogWriter(closeRecordingWriter{onClose: func() { closed = true }})
	if err := l.Init(map[string]any{"persist": true}); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}
	if closed {
		t.Fatal("Close closed the shared request-log store")
	}
}

type closeRecordingWriter struct {
	onClose func()
}

func (closeRecordingWriter) Write(context.Context, requestlog.Entry) error { return nil }
func (w closeRecordingWriter) Close() error {
	w.onClose()
	return nil
}

func TestRequestLogger_ExecuteErrorRedactsKeyInLog(t *testing.T) {
	// Replace the package-level logger with one that captures output to a buffer,
	// so we can verify the logged error message is redacted.
	oldLogger := logger.Default()
	defer func() { logger.SetDefault(oldLogger) }()

	var buf bytes.Buffer
	logger.SetDefault(logger.New(logger.Options{Level: "debug", Output: &buf}))

	l := &RequestLogger{}
	if err := l.Init(map[string]any{}); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Build a fake OpenAI-style key at runtime to avoid credential-scanner false positives.
	fakeKey := "sk-" + strings.Repeat("x", 40)
	pctx := plugin.NewContext(nil)
	pctx.Stage = plugin.StageOnError
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
	pctx.Stage = plugin.StageOnError
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

// A failure is a measurement too. The duration is the only record of whether a
// provider refused instantly or timed out after thirty seconds, and it was
// silently NULL on every on_error row until the error paths populated it.
func TestRequestLogger_PersistsMeasurements(t *testing.T) {
	cases := []struct {
		name         string
		pctx         *plugin.Context
		wantStage    string
		wantDuration *float64
		wantTTFT     *float64
		wantCost     *float64
	}{
		{
			name: "streamed success carries duration, ttft and cost",
			pctx: &plugin.Context{
				Response:     &providers.Response{Model: "gpt-4o", Provider: "openai"},
				Measurements: plugin.Measurements{DurationMs: 950, TTFTMs: 18.5, HasTTFT: true, CostUSD: 0.00042, HasCost: true},
			},
			wantStage: "after_request", wantDuration: f(950), wantTTFT: f(18.5), wantCost: f(0.00042),
		},
		{
			name: "non-streaming success has no time to first token",
			pctx: &plugin.Context{
				Response:     &providers.Response{Model: "gpt-4o", Provider: "openai"},
				Measurements: plugin.Measurements{DurationMs: 21, CostUSD: 0.0002, HasCost: true},
			},
			wantStage: "after_request", wantDuration: f(21), wantTTFT: nil, wantCost: f(0.0002),
		},
		{
			// Zero would claim the request was free, which is a different and
			// much more expensive statement than "we cannot price this model".
			name: "an unpriced model records no cost rather than a cost of zero",
			pctx: &plugin.Context{
				Response:     &providers.Response{Model: "some-local-model", Provider: "ollama"},
				Measurements: plugin.Measurements{DurationMs: 44},
			},
			wantStage: "after_request", wantDuration: f(44), wantTTFT: nil, wantCost: nil,
		},
		{
			name: "a failure records how long it took to fail, and no cost",
			pctx: &plugin.Context{
				Request:      &providers.Request{Model: "gpt-4o"},
				Error:        errors.New("upstream timed out"),
				Measurements: plugin.Measurements{DurationMs: 30000},
			},
			wantStage: "on_error", wantDuration: f(30000), wantTTFT: nil, wantCost: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.pctx.Stage = plugin.Stage(tc.wantStage)
			recorder := &recordingWriter{}
			l := &RequestLogger{}
			l.SetRequestLogWriter(recorder)
			if err := l.Init(map[string]any{"persist": true}); err != nil {
				t.Fatalf("init: %v", err)
			}
			if err := l.Execute(context.Background(), tc.pctx); err != nil {
				t.Fatalf("execute: %v", err)
			}
			if len(recorder.entries) != 1 {
				t.Fatalf("expected 1 entry, got %d", len(recorder.entries))
			}
			got := recorder.entries[0]
			if got.Stage != tc.wantStage {
				t.Fatalf("expected stage %q, got %q", tc.wantStage, got.Stage)
			}
			assertFloatPtr(t, "duration_ms", got.DurationMs, tc.wantDuration)
			assertFloatPtr(t, "ttft_ms", got.TTFTMs, tc.wantTTFT)
			assertFloatPtr(t, "cost_usd", got.CostUSD, tc.wantCost)
		})
	}
}

// Two facts the gateway knows and the row used to lose: which target the request
// went to, and which credential it was served under.
//
// The first is only recoverable from the response, which a failure does not have
// — so an on_error row said something failed and never what. The second is not
// recoverable from the response at all: a cache hit carries the tokens of the
// request that primed the entry, so the row named the provider that produced it
// originally and nothing about whoever consumed it this time.
func TestRequestLogger_PersistsAttribution(t *testing.T) {
	cases := []struct {
		name         string
		pctx         *plugin.Context
		wantStage    string
		wantProvider string
		wantKeyID    string
		wantTokens   int
		wantCost     *float64
	}{
		{
			name: "a failed request names the target it failed on",
			pctx: &plugin.Context{
				Request:  &providers.Request{Model: "gpt-4o"},
				Target:   "openai",
				Error:    errors.New("stream closed mid-response"),
				Metadata: map[string]any{"api_key": "key-a"},
			},
			wantStage: "on_error", wantProvider: "openai", wantKeyID: "key-a",
		},
		{
			// Nothing was attempted — a plugin denied the request, or no target
			// serves the model — so there is no provider to blame and the row
			// says so rather than inventing one.
			name: "a request no target was attempted for names none",
			pctx: &plugin.Context{
				Request:  &providers.Request{Model: "gpt-4o"},
				Error:    errors.New("blocked by policy"),
				Metadata: map[string]any{"api_key": "key-a"},
			},
			wantStage: "on_error", wantProvider: "", wantKeyID: "key-a",
		},
		{
			name: "a provider-served request records the provider and the key",
			pctx: &plugin.Context{
				Response: &providers.Response{
					Model: "gpt-4o", Provider: "openai",
					Usage: providers.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150},
				},
				Target:       "openai",
				Metadata:     map[string]any{"api_key": "key-a"},
				Measurements: plugin.Measurements{DurationMs: 900, CostUSD: 0.00105, HasCost: true},
			},
			wantStage: "after_request", wantProvider: "openai", wantKeyID: "key-a",
			wantTokens: 150, wantCost: f(0.00105),
		},
		{
			// The whole point. The tokens and the provider are the primed
			// entry's; the credential is this request's; the cost is zero
			// because no provider was contacted — a real zero, not the NULL
			// that reads as "we could not price this".
			name: "a cache-served request is attributed to the key that consumed it, at no cost",
			pctx: &plugin.Context{
				Response: &providers.Response{
					Model: "gpt-4o", Provider: "openai",
					Usage: providers.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150},
				},
				SkipProvider: true,
				Metadata:     map[string]any{"api_key": "key-b", "cache_hit": true},
				Measurements: plugin.Measurements{DurationMs: 2, CostUSD: 0, HasCost: true},
			},
			wantStage: "after_request", wantProvider: "openai", wantKeyID: "key-b",
			wantTokens: 150, wantCost: f(0),
		},
		{
			name: "an unauthenticated request records no key",
			pctx: &plugin.Context{
				Response: &providers.Response{Model: "gpt-4o", Provider: "openai"},
				Target:   "openai",
			},
			wantStage: "after_request", wantProvider: "openai", wantKeyID: "",
		},
		{
			name:      "the opening row carries the key before anything has been routed",
			pctx:      &plugin.Context{Request: &providers.Request{Model: "gpt-4o"}, Metadata: map[string]any{"api_key": "key-a"}},
			wantStage: "before_request", wantProvider: "", wantKeyID: "key-a",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.pctx.Stage = plugin.Stage(tc.wantStage)
			recorder := &recordingWriter{}
			l := &RequestLogger{}
			l.SetRequestLogWriter(recorder)
			if err := l.Init(map[string]any{"persist": true}); err != nil {
				t.Fatalf("init: %v", err)
			}
			if err := l.Execute(context.Background(), tc.pctx); err != nil {
				t.Fatalf("execute: %v", err)
			}
			if len(recorder.entries) != 1 {
				t.Fatalf("expected 1 entry, got %d", len(recorder.entries))
			}
			got := recorder.entries[0]
			if got.Stage != tc.wantStage {
				t.Fatalf("stage = %q, want %q", got.Stage, tc.wantStage)
			}
			if got.Provider != tc.wantProvider {
				t.Errorf("provider = %q, want %q", got.Provider, tc.wantProvider)
			}
			if got.APIKeyID != tc.wantKeyID {
				t.Errorf("api_key_id = %q, want %q", got.APIKeyID, tc.wantKeyID)
			}
			if got.TotalTokens != tc.wantTokens {
				t.Errorf("total_tokens = %d, want %d", got.TotalTokens, tc.wantTokens)
			}
			assertFloatPtr(t, "cost_usd", got.CostUSD, tc.wantCost)
		})
	}
}

// failingWriter rejects every write, standing in for a request-log store that
// is down, out of disk, or refusing connections.
type failingWriter struct{ err error }

func (w failingWriter) Write(context.Context, requestlog.Entry) error { return w.err }

// A store that cannot take the row must not take the request with it — a
// logging plugin is failed open by design — but the operator has to learn the
// persisted log is losing rows, so every rejected write is reported with the
// stage it came from.
func TestRequestLogger_PersistFailureIsLoggedAndFailsOpen(t *testing.T) {
	cases := []struct {
		name      string
		pctx      *plugin.Context
		wantStage string
	}{
		{
			name:      "before_request",
			pctx:      plugin.NewContext(&providers.Request{Model: "gpt-4o"}),
			wantStage: "before_request",
		},
		{
			name:      "after_request",
			pctx:      &plugin.Context{Response: &providers.Response{Model: "gpt-4o", Provider: "openai"}},
			wantStage: "after_request",
		},
		{
			name:      "on_error",
			pctx:      &plugin.Context{Request: &providers.Request{Model: "gpt-4o"}, Error: errors.New("upstream timed out")},
			wantStage: "on_error",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.pctx.Stage = plugin.Stage(tc.wantStage)
			oldLogger := logger.Default()
			defer func() { logger.SetDefault(oldLogger) }()

			var buf bytes.Buffer
			logger.SetDefault(logger.New(logger.Options{Level: "debug", Output: &buf}))

			l := &RequestLogger{}
			l.SetRequestLogWriter(failingWriter{err: errors.New("request log store unavailable")})
			if err := l.Init(map[string]any{"persist": true}); err != nil {
				t.Fatalf("init: %v", err)
			}

			if err := l.Execute(context.Background(), tc.pctx); err != nil {
				t.Fatalf("a rejected request-log write must not fail the request: %v", err)
			}

			logged := buf.String()
			if !strings.Contains(logged, "request log write failed") {
				t.Fatalf("a rejected write was dropped silently; log was: %q", logged)
			}
			if !strings.Contains(logged, tc.wantStage) {
				t.Errorf("the warning does not name the stage %q; log was: %q", tc.wantStage, logged)
			}
			if !strings.Contains(logged, "request log store unavailable") {
				t.Errorf("the warning does not carry the store error; log was: %q", logged)
			}
		})
	}
}

func f(v float64) *float64 { return &v }

func assertFloatPtr(t *testing.T, name string, got, want *float64) {
	t.Helper()
	switch {
	case want == nil && got != nil:
		t.Fatalf("%s: expected not measured, got %v", name, *got)
	case want != nil && got == nil:
		t.Fatalf("%s: expected %v, got not measured", name, *want)
	case want != nil && *got != *want:
		t.Fatalf("%s: expected %v, got %v", name, *want, *got)
	}
}
