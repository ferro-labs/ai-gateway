package requestlog

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/sqldb"
)

// user_id and session_id are stored as the caller supplied them and come back
// on List. A row written before migration 6 holds NULL in both, which reads as
// "" — the same answer a request that carried no identity gives — so an old
// database lists without a scan error and without inventing an identity.
func TestSQLiteWriter_WritesAndListsIdentity(t *testing.T) {
	w, err := NewSQLiteWriter(t.Context(), filepath.Join(t.TempDir(), "requests.db"))
	if err != nil {
		t.Fatalf("new sqlite writer: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	base := time.Date(2026, 9, 4, 9, 0, 0, 0, time.UTC)
	written := []Entry{
		{TraceID: "with-identity", Stage: stageAfterRequest, Model: "gpt-4o", Provider: "openai", UserID: "user-42", SessionID: "sess-7", CreatedAt: base},
		{TraceID: "anonymous", Stage: stageAfterRequest, Model: "gpt-4o", Provider: "openai", CreatedAt: base.Add(time.Minute)},
	}
	for _, entry := range written {
		if err := w.Write(t.Context(), entry); err != nil {
			t.Fatalf("write %s: %v", entry.TraceID, err)
		}
	}

	// A pre-migration row: written past Entry so both columns are NULL.
	insert := sqldb.Bind(w.dialect, `INSERT INTO request_logs(trace_id, stage, model, provider, prompt_tokens, completion_tokens, total_tokens, error_message, created_at)
	VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if _, err := w.db.ExecContext(t.Context(), insert,
		"legacy", stageAfterRequest, "gpt-4o", "openai", 0, 0, 0, "", base.Add(2*time.Minute)); err != nil {
		t.Fatalf("insert pre-migration row: %v", err)
	}

	result, err := w.List(t.Context(), Query{Limit: 10})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	got := map[string]Entry{}
	for _, e := range result.Data {
		got[e.TraceID] = e
	}
	if e := got["with-identity"]; e.UserID != "user-42" || e.SessionID != "sess-7" {
		t.Errorf("with-identity row = user %q session %q, want user-42 / sess-7", e.UserID, e.SessionID)
	}
	for _, id := range []string{"anonymous", "legacy"} {
		if e := got[id]; e.UserID != "" || e.SessionID != "" {
			t.Errorf("%s row = user %q session %q, want empty", id, e.UserID, e.SessionID)
		}
	}
}

func TestRequestLogSteps_IdentityIsVersionSix(t *testing.T) {
	steps := requestLogSteps(sqldb.SQLite)
	last := steps[len(steps)-1]
	if last.Version != 6 || last.Name != "request_logs_identity" || last.Fn == nil {
		t.Fatalf("last step = %+v, want version 6 request_logs_identity with a Go body", last)
	}
}
