package requestlog

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSQLiteWriter_WriteListDelete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.db")
	w, err := NewSQLiteWriter(path)
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

	w, err := NewPostgresWriter(dsn)
	if err != nil {
		t.Fatalf("new postgres writer: %v", err)
	}
	t.Cleanup(func() {
		_, _ = w.db.Exec("DELETE FROM request_logs")
		_ = w.Close()
	})

	_, _ = w.db.Exec("DELETE FROM request_logs")

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

	w, err := NewSQLiteWriter("   ")
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
	w, err := NewSQLiteWriter(path)
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
	w, err := NewSQLiteWriter(path)
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
	_, err := NewPostgresWriter("   ")
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
