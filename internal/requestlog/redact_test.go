package requestlog

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/internal/redact"
)

// TestSQLWriterRedactsErrorMessage is the persisted half of F62.
//
// A stored row outlives the process and is served back over GET /admin/logs, so
// it is the sink with the longest reach. Redacting at the writer rather than in
// the plugin that happens to call it means a component recording a raw upstream
// error — including one added later — cannot durably store a credential.
func TestSQLWriterRedactsErrorMessage(t *testing.T) {
	key := strings.Repeat("Ka7bQz3Rt", 3) + "Wm9XcV" // 33 alnum, no prefix
	redact.RegisterSecretValues(key)

	w, err := NewSQLiteWriter(context.Background(), filepath.Join(t.TempDir(), "req.db"))
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	defer func() { _ = w.Close() }()

	raw := "all providers failed: provider mistral attempt 1: mistral API error (401): invalid api_key: " + key
	// Written without any redaction call, exactly as an unaware caller would.
	if err := w.Write(context.Background(), Entry{TraceID: "t1", Stage: "on_error", ErrorMessage: raw}); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := w.List(context.Background(), Query{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got.Data) != 1 {
		t.Fatalf("expected 1 row, got %d", len(got.Data))
	}
	stored := got.Data[0].ErrorMessage
	if strings.Contains(stored, key) {
		t.Fatalf("credential persisted to request_logs: %q", stored)
	}
	if !strings.Contains(stored, "provider mistral attempt 1") {
		t.Fatalf("diagnostic context was lost: %q", stored)
	}
}
