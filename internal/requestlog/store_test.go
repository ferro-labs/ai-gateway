package requestlog

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNewSQLiteWriter_FilePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.db")
	w, err := NewSQLiteWriter(t.Context(), path)
	if err != nil {
		t.Fatalf("new sqlite writer: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat sqlite file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("expected request log file mode 0600, got %o", perm)
	}
}

func TestSQLiteWriter_WriteListDelete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.db")
	w, err := NewSQLiteWriter(t.Context(), path)
	if err != nil {
		t.Fatalf("new sqlite writer: %v", err)
	}
	t.Cleanup(func() {
		_ = w.Close()
	})

	now := time.Now().UTC()
	entries := []Entry{
		{
			TraceID:          "trace-1",
			Stage:            "before_request",
			Model:            "gpt-4o-mini",
			Provider:         "openai",
			PromptTokens:     10,
			CompletionTokens: 0,
			TotalTokens:      10,
			CreatedAt:        now.Add(-2 * time.Hour),
		},
		{
			TraceID:          "trace-2",
			Stage:            "after_request",
			Model:            "gpt-4o-mini",
			Provider:         "openai",
			PromptTokens:     10,
			CompletionTokens: 12,
			TotalTokens:      22,
			CreatedAt:        now.Add(-1 * time.Hour),
		},
		{
			TraceID:          "trace-3",
			Stage:            "on_error",
			Model:            "claude-3-haiku",
			Provider:         "anthropic",
			PromptTokens:     5,
			CompletionTokens: 0,
			TotalTokens:      5,
			ErrorMessage:     "provider timeout",
			CreatedAt:        now,
		},
	}

	for _, entry := range entries {
		if err := w.Write(context.Background(), entry); err != nil {
			t.Fatalf("write request log entry: %v", err)
		}
	}

	result, err := w.List(context.Background(), Query{Limit: 10, Offset: 0})
	if err != nil {
		t.Fatalf("list logs: %v", err)
	}
	if result.Total != 3 || len(result.Data) != 3 {
		t.Fatalf("expected 3 logs, total=%d len=%d", result.Total, len(result.Data))
	}

	filtered, err := w.List(context.Background(), Query{Limit: 10, Offset: 0, Stage: "on_error"})
	if err != nil {
		t.Fatalf("list filtered logs: %v", err)
	}
	if filtered.Total != 1 || len(filtered.Data) != 1 {
		t.Fatalf("expected 1 on_error log, total=%d len=%d", filtered.Total, len(filtered.Data))
	}
	if filtered.Data[0].TraceID != "trace-3" {
		t.Fatalf("unexpected filtered trace id: %s", filtered.Data[0].TraceID)
	}

	deleted, err := w.Delete(context.Background(), MaintenanceQuery{Before: ptrTime(now.Add(-30 * time.Minute))})
	if err != nil {
		t.Fatalf("delete logs: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("expected deleted=2, got %d", deleted)
	}

	remaining, err := w.List(context.Background(), Query{Limit: 10, Offset: 0})
	if err != nil {
		t.Fatalf("list remaining logs: %v", err)
	}
	if remaining.Total != 1 || len(remaining.Data) != 1 {
		t.Fatalf("expected 1 remaining log, total=%d len=%d", remaining.Total, len(remaining.Data))
	}
	if remaining.Data[0].TraceID != "trace-3" {
		t.Fatalf("unexpected remaining trace id: %s", remaining.Data[0].TraceID)
	}
}

func TestPostgresWriterContract(t *testing.T) {
	dsn := os.Getenv("FERROGW_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set FERROGW_TEST_POSTGRES_DSN to run Postgres requestlog integration tests")
	}

	w, err := NewPostgresWriter(t.Context(), dsn)
	if err != nil {
		t.Fatalf("new postgres writer: %v", err)
	}
	t.Cleanup(func() {
		// t.Context() is already canceled by the time Cleanup runs; use a
		// fresh context for this cleanup query.
		_, _ = w.db.ExecContext(context.Background(), "DELETE FROM request_logs")
		_ = w.Close()
	})

	_, _ = w.db.ExecContext(t.Context(), "DELETE FROM request_logs")

	entry := Entry{
		TraceID:          "pg-trace",
		Stage:            "after_request",
		Model:            "gpt-4o-mini",
		Provider:         "openai",
		PromptTokens:     7,
		CompletionTokens: 9,
		TotalTokens:      16,
		CreatedAt:        time.Now().UTC(),
	}
	if err := w.Write(context.Background(), entry); err != nil {
		t.Fatalf("write postgres log: %v", err)
	}

	result, err := w.List(context.Background(), Query{Limit: 10, Offset: 0, Provider: "openai"})
	if err != nil {
		t.Fatalf("list postgres logs: %v", err)
	}
	if result.Total != 1 || len(result.Data) != 1 {
		t.Fatalf("expected 1 postgres log, total=%d len=%d", result.Total, len(result.Data))
	}
}

func TestSQLiteWriter_DefaultDSNAndZeroCreatedAt(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get wd: %v", err)
	}

	tempDir := t.TempDir()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir temp dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(wd)
	})

	w, err := NewSQLiteWriter(t.Context(), "   ")
	if err != nil {
		t.Fatalf("new sqlite writer with default dsn: %v", err)
	}
	t.Cleanup(func() {
		_ = w.Close()
	})

	if _, err := os.Stat("ferrogw-requests.db"); err != nil {
		t.Fatalf("expected default sqlite file to exist: %v", err)
	}

	if err := w.Write(context.Background(), Entry{
		TraceID: "trace-default",
		Stage:   "before_request",
	}); err != nil {
		t.Fatalf("write default sqlite log: %v", err)
	}

	result, err := w.List(context.Background(), Query{Stage: "before_request"})
	if err != nil {
		t.Fatalf("list default sqlite logs: %v", err)
	}
	if result.Total != 1 || len(result.Data) != 1 {
		t.Fatalf("expected 1 log, total=%d len=%d", result.Total, len(result.Data))
	}
	if result.Data[0].CreatedAt.IsZero() {
		t.Fatal("expected CreatedAt to be auto-populated")
	}
}

func TestSQLiteWriter_ListDefaultsAndSinceFilter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.db")
	w, err := NewSQLiteWriter(t.Context(), path)
	if err != nil {
		t.Fatalf("new sqlite writer: %v", err)
	}
	t.Cleanup(func() {
		_ = w.Close()
	})

	base := time.Now().UTC().Add(-10 * time.Minute)
	entries := []Entry{
		{
			TraceID:   "trace-1",
			Stage:     "before_request",
			Model:     "gpt-4o-mini",
			Provider:  "openai",
			CreatedAt: base,
		},
		{
			TraceID:   "trace-2",
			Stage:     "after_request",
			Model:     "claude-3-5-sonnet",
			Provider:  "anthropic",
			CreatedAt: base.Add(5 * time.Minute),
		},
	}

	for _, entry := range entries {
		if err := w.Write(context.Background(), entry); err != nil {
			t.Fatalf("write request log entry: %v", err)
		}
	}

	since := base.Add(1 * time.Minute)
	result, err := w.List(context.Background(), Query{
		Limit:    999,
		Offset:   -10,
		Model:    "claude-3-5-sonnet",
		Provider: "anthropic",
		Since:    &since,
	})
	if err != nil {
		t.Fatalf("list filtered logs: %v", err)
	}
	if result.Total != 1 || len(result.Data) != 1 {
		t.Fatalf("expected 1 filtered log, total=%d len=%d", result.Total, len(result.Data))
	}
	if result.Data[0].TraceID != "trace-2" {
		t.Fatalf("unexpected trace id: %s", result.Data[0].TraceID)
	}
}

func TestDeleteRequiresBefore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.db")
	w, err := NewSQLiteWriter(t.Context(), path)
	if err != nil {
		t.Fatalf("new sqlite writer: %v", err)
	}
	t.Cleanup(func() {
		_ = w.Close()
	})

	_, err = w.Delete(context.Background(), MaintenanceQuery{})
	if err == nil || !strings.Contains(err.Error(), "before is required") {
		t.Fatalf("expected before is required error, got %v", err)
	}
}

func TestNewPostgresWriterRequiresDSN(t *testing.T) {
	_, err := NewPostgresWriter(t.Context(), "   ")
	if err == nil || !strings.Contains(err.Error(), "postgres dsn is required") {
		t.Fatalf("expected postgres dsn required error, got %v", err)
	}
}

func TestNoopWriterBindPostgresAndClose(t *testing.T) {
	if err := (NoopWriter{}).Write(context.Background(), Entry{}); err != nil {
		t.Fatalf("noop writer returned error: %v", err)
	}

	got := bindPostgres("SELECT * FROM request_logs WHERE stage = ? AND model = ? LIMIT ? OFFSET ?")
	want := "SELECT * FROM request_logs WHERE stage = $1 AND model = $2 LIMIT $3 OFFSET $4"
	if got != want {
		t.Fatalf("unexpected postgres bind query:\nwant: %s\ngot:  %s", want, got)
	}

	var w *SQLWriter
	if err := w.Close(); err != nil {
		t.Fatalf("nil writer close returned error: %v", err)
	}
}

func ptrTime(t time.Time) *time.Time {
	return &t
}

// insertRawLog inserts a row with nullable model/provider/errMsg (pass nil for
// SQL NULL, a string for a value) so Stats can be tested against real NULLs that
// Write cannot produce.
func insertRawLog(t *testing.T, w *SQLWriter, stage string, model, provider, errMsg any, tokens int, createdAt time.Time) {
	t.Helper()
	_, err := w.db.ExecContext(context.Background(),
		`INSERT INTO request_logs(trace_id, stage, model, provider, prompt_tokens, completion_tokens, total_tokens, error_message, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"trace", stage, model, provider, 0, 0, tokens, errMsg, createdAt)
	if err != nil {
		t.Fatalf("insert raw log: %v", err)
	}
}

func newStatsFixture(t *testing.T) (*SQLWriter, time.Time) {
	t.Helper()
	w, err := NewSQLiteWriter(t.Context(), filepath.Join(t.TempDir(), "stats.db"))
	if err != nil {
		t.Fatalf("new sqlite writer: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	base := time.Now().UTC().Truncate(time.Second)
	// (stage, model, provider, errMsg, tokens, createdAt)
	insertRawLog(t, w, "after_request", "gpt-4", "openai", nil, 10, base.Add(1*time.Minute))
	insertRawLog(t, w, "after_request", "gpt-4", "openai", "", 20, base.Add(2*time.Minute)) // empty err: not an error
	insertRawLog(t, w, "on_error", "gpt-4", "openai", nil, 5, base.Add(3*time.Minute))      // on_error + NULL err: error
	insertRawLog(t, w, "after_request", "claude", "anthropic", "boom", 7, base.Add(4*time.Minute))
	insertRawLog(t, w, "after_request", nil, nil, nil, 3, base.Add(5*time.Minute)) // NULL model+provider -> unknown
	insertRawLog(t, w, "after_request", "", "", nil, 4, base.Add(6*time.Minute))   // '' model+provider -> unknown (merges with NULL)
	return w, base
}

func TestSQLiteWriter_Stats(t *testing.T) {
	w, base := newStatsFixture(t)

	// Unfiltered aggregate. TotalEntries/ErrorEntries/TotalTokens match the
	// output of the previous in-Go aggregation over the same fixture: 6 rows,
	// 2 errors (the on_error+NULL row and the non-empty-error row), 49 tokens.
	got, err := w.Stats(context.Background(), Query{})
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	want := StatsResult{
		TotalEntries: 6,
		ErrorEntries: 2,
		TotalTokens:  49,
		ByStage:      map[string]int{"after_request": 5, "on_error": 1},
		ByProvider:   map[string]int{"openai": 3, "anthropic": 1, "unknown": 2},
		ByModel:      map[string]int{"gpt-4": 3, "claude": 1, "unknown": 2},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unfiltered stats mismatch:\n got: %+v\nwant: %+v", got, want)
	}

	cases := []struct {
		name  string
		query Query
		want  StatsResult
	}{
		{
			name:  "provider filter",
			query: Query{Provider: "openai"},
			want: StatsResult{
				TotalEntries: 3, ErrorEntries: 1, TotalTokens: 35,
				ByStage:    map[string]int{"after_request": 2, "on_error": 1},
				ByProvider: map[string]int{"openai": 3},
				ByModel:    map[string]int{"gpt-4": 3},
			},
		},
		{
			name:  "stage filter",
			query: Query{Stage: "on_error"},
			want: StatsResult{
				TotalEntries: 1, ErrorEntries: 1, TotalTokens: 5,
				ByStage:    map[string]int{"on_error": 1},
				ByProvider: map[string]int{"openai": 1},
				ByModel:    map[string]int{"gpt-4": 1},
			},
		},
		{
			name:  "model filter",
			query: Query{Model: "gpt-4"},
			want: StatsResult{
				TotalEntries: 3, ErrorEntries: 1, TotalTokens: 35,
				ByStage:    map[string]int{"after_request": 2, "on_error": 1},
				ByProvider: map[string]int{"openai": 3},
				ByModel:    map[string]int{"gpt-4": 3},
			},
		},
		{
			name:  "since filter",
			query: Query{Since: ptrTime(base.Add(3*time.Minute + 30*time.Second))}, // rows 4,5,6
			want: StatsResult{
				TotalEntries: 3, ErrorEntries: 1, TotalTokens: 14,
				ByStage:    map[string]int{"after_request": 3},
				ByProvider: map[string]int{"anthropic": 1, "unknown": 2},
				ByModel:    map[string]int{"claude": 1, "unknown": 2},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := w.Stats(context.Background(), tc.query)
			if err != nil {
				t.Fatalf("stats: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("stats mismatch:\n got: %+v\nwant: %+v", got, tc.want)
			}
		})
	}
}

func TestSQLiteWriter_Stats_EmptyTable(t *testing.T) {
	w, err := NewSQLiteWriter(t.Context(), filepath.Join(t.TempDir(), "empty.db"))
	if err != nil {
		t.Fatalf("new sqlite writer: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	got, err := w.Stats(context.Background(), Query{})
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	want := StatsResult{
		ByStage:    map[string]int{},
		ByProvider: map[string]int{},
		ByModel:    map[string]int{},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("empty stats mismatch:\n got: %+v\nwant: %+v", got, want)
	}
	// Maps must be non-nil so the handler JSON-encodes {} rather than null.
	if got.ByStage == nil || got.ByProvider == nil || got.ByModel == nil {
		t.Fatal("expected non-nil maps in empty StatsResult")
	}
}

func TestSQLiteWriter_CreatedAtIndexExists(t *testing.T) {
	w, err := NewSQLiteWriter(t.Context(), filepath.Join(t.TempDir(), "idx.db"))
	if err != nil {
		t.Fatalf("new sqlite writer: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	var name string
	err = w.db.QueryRowContext(context.Background(),
		"SELECT name FROM sqlite_master WHERE type='index' AND name='idx_request_logs_created_at'").Scan(&name)
	if err != nil {
		t.Fatalf("expected created_at index to exist: %v", err)
	}
	if name != "idx_request_logs_created_at" {
		t.Fatalf("unexpected index name: %s", name)
	}
}
