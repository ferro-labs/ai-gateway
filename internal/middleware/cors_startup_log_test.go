package middleware

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/pkg/logger"
)

// captureDefaultLogger redirects the process default logger into a buffer for
// the duration of one test.
func captureDefaultLogger(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	previous := logger.Default()
	logger.SetDefault(logger.New(logger.Options{Level: "debug", Output: buf}))
	t.Cleanup(func() { logger.SetDefault(previous) })
	return buf
}

func logLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("log line is not JSON: %q: %v", line, err)
		}
		out = append(out, entry)
	}
	return out
}

// TestCORS_NoOrigins_LogsAtInfoNotWarn pins the severity of the startup line.
// The bundled dashboard shares this origin, so an unset CORS_ORIGINS is the
// correct default state, and a warning on every correct start is noise that
// costs the operator's attention for the warnings that do matter.
func TestCORS_NoOrigins_LogsAtInfoNotWarn(t *testing.T) {
	buf := captureDefaultLogger(t)

	_ = CORS()

	entries := logLines(t, buf)
	if len(entries) != 1 {
		t.Fatalf("want exactly one startup log line, got %d: %s", len(entries), buf.String())
	}
	if level, _ := entries[0]["level"].(string); level != "INFO" {
		t.Errorf("startup line level = %q, want INFO: %s", level, buf.String())
	}

	msg, _ := entries[0]["msg"].(string)
	// The pre-v1.4.0 text pointed the operator at CORS_ORIGINS "for your
	// dashboard", which no longer needs one.
	if strings.Contains(msg, "e.g. your dashboard") {
		t.Errorf("startup line still advises setting CORS_ORIGINS for the dashboard: %q", msg)
	}
	if !strings.Contains(msg, "CORS_ORIGINS") {
		t.Errorf("startup line should still name the setting it describes: %q", msg)
	}
}

// TestCORS_WithOrigins_LogsNothing keeps the line scoped to the unset case.
func TestCORS_WithOrigins_LogsNothing(t *testing.T) {
	buf := captureDefaultLogger(t)

	_ = CORS("https://app.example.com")

	if got := strings.TrimSpace(buf.String()); got != "" {
		t.Errorf("configured allowlist should log nothing at startup, got: %s", got)
	}
}
