package requestlog

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func sqliteObjectExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var got string
	err := db.QueryRowContext(context.Background(),
		"SELECT name FROM sqlite_master WHERE name = ?", name).Scan(&got)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		t.Fatalf("probe sqlite object %q: %v", name, err)
	}
	return true
}

// TestSQLiteWriter_MigrationSchema confirms the request log store runs on the
// migration runner: its own ledger table is present alongside the created_at
// index after construction.
func TestSQLiteWriter_MigrationSchema(t *testing.T) {
	w, err := NewSQLiteWriter(t.Context(), filepath.Join(t.TempDir(), "requests.db"))
	if err != nil {
		t.Fatalf("new sqlite writer: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	for _, name := range []string{requestLogLedger, "request_logs", createdAtIndex} {
		if !sqliteObjectExists(t, w.db, name) {
			t.Errorf("expected %q to exist after construction", name)
		}
	}
}

// TestSQLiteWriter_AdoptsPreRunnerDatabase confirms a request_logs database
// created before the runner existed is adopted at the baseline (not re-created)
// and gains the created_at index, with its existing rows still listable.
func TestSQLiteWriter_AdoptsPreRunnerDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-requests.db")

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	if _, err := raw.ExecContext(context.Background(), `
CREATE TABLE request_logs (
	id INTEGER PRIMARY KEY,
	trace_id TEXT,
	stage TEXT NOT NULL,
	model TEXT,
	provider TEXT,
	prompt_tokens INTEGER NOT NULL,
	completion_tokens INTEGER NOT NULL,
	total_tokens INTEGER NOT NULL,
	error_message TEXT,
	created_at TIMESTAMP NOT NULL
)`); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if _, err := raw.ExecContext(context.Background(),
		`INSERT INTO request_logs(trace_id, stage, model, provider, prompt_tokens, completion_tokens, total_tokens, error_message, created_at)
		VALUES('legacy-trace', 'after_request', 'gpt-4', 'openai', 1, 2, 3, '', ?)`, time.Now().UTC()); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw sqlite: %v", err)
	}

	w, err := NewSQLiteWriter(t.Context(), path)
	if err != nil {
		t.Fatalf("open writer on legacy db: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	result, err := w.List(context.Background(), Query{Limit: 10})
	if err != nil {
		t.Fatalf("list legacy logs: %v", err)
	}
	if result.Total != 1 || len(result.Data) != 1 || result.Data[0].TraceID != "legacy-trace" {
		t.Fatalf("expected the pre-existing legacy row, got total=%d data=%+v", result.Total, result.Data)
	}
	if !sqliteObjectExists(t, w.db, createdAtIndex) {
		t.Error("created_at index was not built for an adopted legacy database")
	}
}

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

func TestNoopWriterAndNilClose(t *testing.T) {
	if err := (NoopWriter{}).Write(context.Background(), Entry{}); err != nil {
		t.Fatalf("noop writer returned error: %v", err)
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

// stat keeps the expectation tables readable now that a dimension group carries
// its error, token and cost totals alongside its count.
//
// Every row in the shared fixture is written through insertRawLog, which sets no
// cost, so each group's unpriced count equals its row count. Pricing is covered
// by TestSQLiteWriter_Stats_Cost, which inserts rows that carry one.
func stat(count, errors, tokens int) DimensionStat {
	return DimensionStat{Count: count, Errors: errors, Tokens: tokens, Unpriced: count}
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
		TotalEntries:     6,
		ErrorEntries:     2,
		TotalTokens:      49,
		UnpricedRequests: 6,
		ByStage:          map[string]DimensionStat{"after_request": stat(5, 1, 44), "on_error": stat(1, 1, 5)},
		ByProvider:       map[string]DimensionStat{"openai": stat(3, 1, 35), "anthropic": stat(1, 1, 7), "unknown": stat(2, 0, 7)},
		ByModel:          map[string]DimensionStat{"gpt-4": stat(3, 1, 35), "claude": stat(1, 1, 7), "unknown": stat(2, 0, 7)},
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
				TotalEntries: 3, ErrorEntries: 1, TotalTokens: 35, UnpricedRequests: 3,
				ByStage:    map[string]DimensionStat{"after_request": stat(2, 0, 30), "on_error": stat(1, 1, 5)},
				ByProvider: map[string]DimensionStat{"openai": stat(3, 1, 35)},
				ByModel:    map[string]DimensionStat{"gpt-4": stat(3, 1, 35)},
			},
		},
		{
			name:  "stage filter",
			query: Query{Stage: "on_error"},
			want: StatsResult{
				TotalEntries: 1, ErrorEntries: 1, TotalTokens: 5, UnpricedRequests: 1,
				ByStage:    map[string]DimensionStat{"on_error": stat(1, 1, 5)},
				ByProvider: map[string]DimensionStat{"openai": stat(1, 1, 5)},
				ByModel:    map[string]DimensionStat{"gpt-4": stat(1, 1, 5)},
			},
		},
		{
			name:  "model filter",
			query: Query{Model: "gpt-4"},
			want: StatsResult{
				TotalEntries: 3, ErrorEntries: 1, TotalTokens: 35, UnpricedRequests: 3,
				ByStage:    map[string]DimensionStat{"after_request": stat(2, 0, 30), "on_error": stat(1, 1, 5)},
				ByProvider: map[string]DimensionStat{"openai": stat(3, 1, 35)},
				ByModel:    map[string]DimensionStat{"gpt-4": stat(3, 1, 35)},
			},
		},
		{
			name:  "since filter",
			query: Query{Since: ptrTime(base.Add(3*time.Minute + 30*time.Second))}, // rows 4,5,6
			want: StatsResult{
				TotalEntries: 3, ErrorEntries: 1, TotalTokens: 14, UnpricedRequests: 3,
				ByStage:    map[string]DimensionStat{"after_request": stat(3, 1, 14)},
				ByProvider: map[string]DimensionStat{"anthropic": stat(1, 1, 7), "unknown": stat(2, 0, 7)},
				ByModel:    map[string]DimensionStat{"claude": stat(1, 1, 7), "unknown": stat(2, 0, 7)},
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
		ByStage:    map[string]DimensionStat{},
		ByProvider: map[string]DimensionStat{},
		ByModel:    map[string]DimensionStat{},
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

// seriesFixture writes rows one minute apart ending at `end`, so a series over
// the window has something to bucket. Each request contributes the two rows the
// logger really writes: a before_request row and a terminal one.
func seriesFixture(t *testing.T, w *SQLWriter, end time.Time, requests int, failEvery int) {
	t.Helper()
	for i := range requests {
		at := end.Add(-time.Duration(requests-i) * time.Minute)
		insertRawLog(t, w, "before_request", "gpt-4", nil, nil, 0, at)
		if failEvery > 0 && i%failEvery == 0 {
			insertRawLog(t, w, "on_error", "gpt-4", "openai", "upstream exploded", 0, at)
			continue
		}
		insertRawLogTokens(t, w, "after_request", "gpt-4", "openai", nil, 10, 4, at)
	}
}

// insertRawLogTokens is insertRawLog with the prompt/completion split set, which
// the series and the token summary both read and insertRawLog leaves at zero.
func insertRawLogTokens(t *testing.T, w *SQLWriter, stage string, model, provider, errMsg any, prompt, completion int, createdAt time.Time) {
	t.Helper()
	_, err := w.db.ExecContext(context.Background(),
		`INSERT INTO request_logs(trace_id, stage, model, provider, prompt_tokens, completion_tokens, total_tokens, error_message, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"trace", stage, model, provider, prompt, completion, prompt+completion, errMsg, createdAt)
	if err != nil {
		t.Fatalf("insert raw log: %v", err)
	}
}

func TestSQLiteWriter_Stats_Series(t *testing.T) {
	w, err := NewSQLiteWriter(t.Context(), filepath.Join(t.TempDir(), "series.db"))
	if err != nil {
		t.Fatalf("new sqlite writer: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	now := time.Now().UTC()
	seriesFixture(t, w, now, 10, 5) // 10 requests, every 5th fails => 2 failures

	since := now.Add(-30 * time.Minute)
	got, err := w.Stats(context.Background(), Query{Since: &since, SeriesBuckets: 6})
	if err != nil {
		t.Fatalf("stats: %v", err)
	}

	if len(got.Series) != 6 {
		t.Fatalf("expected 6 buckets, got %d", len(got.Series))
	}
	if got.SeriesTruncated {
		t.Fatal("a fixture well under the scan limit must not report truncation")
	}

	var requests, errors, prompt, completion int
	for _, point := range got.Series {
		requests += point.Requests
		errors += point.Errors
		prompt += point.PromptTokens
		completion += point.CompletionTokens
	}

	// 10 requests, not the 20 rows they wrote: a before_request row is the same
	// request counted a second time, and counting rows would double the traffic.
	if requests != 10 {
		t.Fatalf("expected 10 requests across the series, got %d", requests)
	}
	if errors != 2 {
		t.Fatalf("expected 2 errors across the series, got %d", errors)
	}
	if prompt != 80 || completion != 32 {
		t.Fatalf("expected 8 successful requests at 10/4 tokens, got prompt=%d completion=%d", prompt, completion)
	}

	// Buckets ascend in time and tile the window without gaps or overlap.
	for i := 1; i < len(got.Series); i++ {
		if !got.Series[i].Start.After(got.Series[i-1].Start) {
			t.Fatalf("bucket %d does not start after bucket %d", i, i-1)
		}
	}
	if got.Series[0].Start.Before(got.SeriesStart) {
		t.Fatal("the first bucket starts before the reported window")
	}

	// The summary and the series must agree: both count a request as a terminal
	// stage, so a chart cannot disagree with the figure printed above it.
	summaryRequests := got.ByStage["after_request"].Count + got.ByStage["on_error"].Count
	if summaryRequests != requests {
		t.Fatalf("series counts %d requests but the stage summary counts %d", requests, summaryRequests)
	}
}

func TestSQLiteWriter_Stats_SeriesTruncationClampsWindow(t *testing.T) {
	w, err := NewSQLiteWriter(t.Context(), filepath.Join(t.TempDir(), "trunc.db"))
	if err != nil {
		t.Fatalf("new sqlite writer: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	original := seriesScanLimit
	seriesScanLimit = 4
	t.Cleanup(func() { seriesScanLimit = original })

	now := time.Now().UTC()
	seriesFixture(t, w, now, 10, 0) // 20 rows, far more than the lowered cap

	since := now.Add(-24 * time.Hour)
	got, err := w.Stats(context.Background(), Query{Since: &since, SeriesBuckets: 4})
	if err != nil {
		t.Fatalf("stats: %v", err)
	}

	if !got.SeriesTruncated {
		t.Fatal("a scan that hit the cap must report truncation")
	}
	// The window is clamped to the rows actually scanned. Left at the requested
	// 24 hours, the ~4 minutes of data would occupy one bucket and the other
	// three would render as zero traffic — an outage rather than missing data.
	if got.SeriesStart.Before(now.Add(-time.Hour)) {
		t.Fatalf("expected the window clamped to the scanned rows, got start %s for a scan covering minutes", got.SeriesStart)
	}
	if !got.SeriesEnd.After(got.SeriesStart) {
		t.Fatalf("expected end after start, got %s..%s", got.SeriesStart, got.SeriesEnd)
	}
}

func TestSQLiteWriter_Stats_TopErrors(t *testing.T) {
	w, err := NewSQLiteWriter(t.Context(), filepath.Join(t.TempDir(), "errors.db"))
	if err != nil {
		t.Fatalf("new sqlite writer: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	now := time.Now().UTC()
	for range 3 {
		insertRawLog(t, w, "on_error", "gpt-4", "openai", "rate limited", 0, now)
	}
	insertRawLog(t, w, "on_error", "gpt-4", "openai", "context length exceeded", 0, now)
	// Neither of these carries a failure message, and a successful row must not
	// rank as one: the empty string would otherwise be the commonest "error".
	insertRawLog(t, w, "after_request", "gpt-4", "openai", "", 5, now)
	insertRawLog(t, w, "after_request", "gpt-4", "openai", nil, 5, now)

	got, err := w.Stats(context.Background(), Query{TopErrors: 5})
	if err != nil {
		t.Fatalf("stats: %v", err)
	}

	want := []ErrorGroup{
		{Message: "rate limited", Count: 3},
		{Message: "context length exceeded", Count: 1},
	}
	if !reflect.DeepEqual(got.TopErrors, want) {
		t.Fatalf("top errors mismatch:\n got: %+v\nwant: %+v", got.TopErrors, want)
	}
}

func TestSQLiteWriter_Stats_OmitsSeriesAndErrorsUnlessAsked(t *testing.T) {
	w, base := newStatsFixture(t)
	since := base
	got, err := w.Stats(context.Background(), Query{Since: &since})
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if got.Series != nil {
		t.Fatalf("expected no series without SeriesBuckets, got %d points", len(got.Series))
	}
	if got.TopErrors != nil {
		t.Fatalf("expected no error ranking without TopErrors, got %+v", got.TopErrors)
	}

	// Without Since there is no window to divide, so buckets alone must not
	// produce a series anchored on an arbitrary start.
	got, err = w.Stats(context.Background(), Query{SeriesBuckets: 6})
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if got.Series != nil {
		t.Fatalf("expected no series without Since, got %d points", len(got.Series))
	}
}

func TestSQLiteWriter_Write_NormalisesTimestampToUTC(t *testing.T) {
	w, err := NewSQLiteWriter(t.Context(), filepath.Join(t.TempDir(), "utc.db"))
	if err != nil {
		t.Fatalf("new sqlite writer: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	// A row written with a +05:30 offset has to land in the same window as one
	// written in UTC. The driver stores a time.Time as text and range filters
	// compare that text, so an un-normalised offset would place the row
	// somewhere its own timestamp does not claim to be. Asserted through a
	// query rather than by reading the stored string, which is the driver's
	// business and not a contract.
	zone := time.FixedZone("IST", 5*60*60+30*60)
	local := time.Now().In(zone)
	if err := w.Write(context.Background(), Entry{Stage: "after_request", TraceID: "local", CreatedAt: local}); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := w.List(context.Background(), Query{Limit: 10})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got.Data) != 1 {
		t.Fatalf("expected 1 row, got %d", len(got.Data))
	}
	if !got.Data[0].CreatedAt.Equal(local) {
		t.Fatalf("timestamp did not round-trip: wrote %s, read %s", local, got.Data[0].CreatedAt)
	}

	// And it is findable by a UTC-anchored range, which is how every caller asks.
	since := local.UTC().Add(-time.Minute)
	found, err := w.List(context.Background(), Query{Limit: 10, Since: &since})
	if err != nil {
		t.Fatalf("list since: %v", err)
	}
	if found.Total != 1 {
		t.Fatalf("a row written in a non-UTC zone was not found by a UTC range filter (total=%d)", found.Total)
	}
}

func floatPtr(value float64) *float64 { return &value }

func TestSQLiteWriter_Stats_Cost(t *testing.T) {
	w, err := NewSQLiteWriter(t.Context(), filepath.Join(t.TempDir(), "cost.db"))
	if err != nil {
		t.Fatalf("new sqlite writer: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	now := time.Now().UTC()
	write := func(model string, cost *float64) {
		t.Helper()
		if err := w.Write(context.Background(), Entry{
			Stage: "after_request", Model: model, Provider: "openai",
			PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15,
			CostUSD: cost, CreatedAt: now,
		}); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	write("gpt-4o", floatPtr(0.02))
	write("gpt-4o", floatPtr(0.03))
	// A model the catalog does not price. Its cost is unknown, not zero, and
	// counting it as free would understate every spend figure derived from it.
	write("some-local-model", nil)

	got, err := w.Stats(context.Background(), Query{})
	if err != nil {
		t.Fatalf("stats: %v", err)
	}

	if math.Abs(got.TotalCostUSD-0.05) > 1e-9 {
		t.Fatalf("expected total cost 0.05, got %v", got.TotalCostUSD)
	}
	if got.UnpricedRequests != 1 {
		t.Fatalf("expected 1 unpriced request, got %d", got.UnpricedRequests)
	}
	if priced := got.ByModel["gpt-4o"]; math.Abs(priced.CostUSD-0.05) > 1e-9 || priced.Unpriced != 0 {
		t.Fatalf("unexpected priced model stat: %+v", priced)
	}
	// The unpriced model contributes a row and tokens but no spend, and says so
	// rather than reporting a confident zero.
	if unpriced := got.ByModel["some-local-model"]; unpriced.CostUSD != 0 || unpriced.Unpriced != 1 {
		t.Fatalf("unexpected unpriced model stat: %+v", unpriced)
	}
}

func TestSQLiteWriter_Stats_LatencyPercentiles(t *testing.T) {
	w, err := NewSQLiteWriter(t.Context(), filepath.Join(t.TempDir(), "latency.db"))
	if err != nil {
		t.Fatalf("new sqlite writer: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	now := time.Now().UTC()
	// 1..100ms, and only the first ten carry a time to first token — the rest
	// stand in for non-streaming requests, which have none.
	for i := 1; i <= 100; i++ {
		entry := Entry{
			Stage: "after_request", Model: "gpt-4o", Provider: "openai",
			CreatedAt:  now.Add(-time.Duration(i) * time.Second),
			DurationMs: floatPtr(float64(i)),
		}
		if i <= 10 {
			entry.TTFTMs = floatPtr(float64(i) * 2)
		}
		if err := w.Write(context.Background(), entry); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	since := now.Add(-10 * time.Minute)
	got, err := w.Stats(context.Background(), Query{Since: &since, SeriesBuckets: 4})
	if err != nil {
		t.Fatalf("stats: %v", err)
	}

	if got.LatencyMs == nil {
		t.Fatal("expected latency percentiles")
	}
	if got.LatencyMs.Count != 100 {
		t.Fatalf("expected 100 measured durations, got %d", got.LatencyMs.Count)
	}
	// Nearest-rank over 1..100: p50 = 50, p95 = 95, p99 = 99.
	if got.LatencyMs.P50 != 50 || got.LatencyMs.P95 != 95 || got.LatencyMs.P99 != 99 || got.LatencyMs.Max != 100 {
		t.Fatalf("unexpected latency percentiles: %+v", got.LatencyMs)
	}
	if math.Abs(got.LatencyMs.Mean-50.5) > 1e-9 {
		t.Fatalf("expected mean 50.5, got %v", got.LatencyMs.Mean)
	}

	// TTFT summarises only the rows that had one. Counting the ninety rows
	// without a first token as zero would report a gateway answering instantly.
	if got.TTFTMs == nil || got.TTFTMs.Count != 10 {
		t.Fatalf("expected 10 measured TTFTs, got %+v", got.TTFTMs)
	}
	if got.TTFTMs.P50 != 10 || got.TTFTMs.Max != 20 {
		t.Fatalf("unexpected TTFT percentiles: %+v", got.TTFTMs)
	}
}

func TestSQLiteWriter_Stats_NoPercentilesWithoutMeasurements(t *testing.T) {
	w, base := newStatsFixture(t)
	since := base
	got, err := w.Stats(context.Background(), Query{Since: &since, SeriesBuckets: 4})
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	// The fixture predates the measurement columns, exactly like rows written
	// before the migration. Nil, not a zero-valued struct: a gateway that has
	// recorded no duration has no median, and 0ms would read as a fast one.
	if got.LatencyMs != nil {
		t.Fatalf("expected no latency percentiles, got %+v", got.LatencyMs)
	}
	if got.TTFTMs != nil {
		t.Fatalf("expected no TTFT percentiles, got %+v", got.TTFTMs)
	}
}

func TestSQLiteWriter_Write_PreservesNullMeasurements(t *testing.T) {
	w, err := NewSQLiteWriter(t.Context(), filepath.Join(t.TempDir(), "nulls.db"))
	if err != nil {
		t.Fatalf("new sqlite writer: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	if err := w.Write(context.Background(), Entry{
		Stage: "after_request", Model: "m", Provider: "p",
		CreatedAt: time.Now().UTC(), DurationMs: floatPtr(12.5),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := w.List(context.Background(), Query{Limit: 1})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	entry := got.Data[0]
	if entry.DurationMs == nil || *entry.DurationMs != 12.5 {
		t.Fatalf("duration did not round-trip: %+v", entry.DurationMs)
	}
	// Round-tripped as absent rather than as zero, which is what lets the API
	// report "not measured" instead of an instant, free request.
	if entry.TTFTMs != nil || entry.CostUSD != nil {
		t.Fatalf("expected unmeasured fields to stay nil, got ttft=%+v cost=%+v", entry.TTFTMs, entry.CostUSD)
	}
}

// The credential a request was served under is the one attribution a row cannot
// derive from anything else it holds. A response served from the shared cache
// carries the provider and the token counts of the request that PRIMED the
// entry, so without this column the row said who produced the answer and
// nothing at all about who consumed it.
func TestSQLiteWriter_APIKeyRoundTrip(t *testing.T) {
	w, err := NewSQLiteWriter(t.Context(), filepath.Join(t.TempDir(), "requests.db"))
	if err != nil {
		t.Fatalf("new sqlite writer: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	zero := 0.0
	cases := []struct {
		name  string
		entry Entry
		want  string
	}{
		{
			name: "a provider-served row keeps the key that paid for it",
			entry: Entry{
				TraceID: "provider-served", Stage: "after_request", Model: "gpt-4o", Provider: "openai",
				APIKeyID: "key-a", PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150,
			},
			want: "key-a",
		},
		{
			name: "a cache-served row names the key it was served to, at a cost of zero",
			entry: Entry{
				TraceID: "cache-served", Stage: "after_request", Model: "gpt-4o", Provider: "openai",
				APIKeyID: "key-b", PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150, CostUSD: &zero,
			},
			want: "key-b",
		},
		{
			name: "a failure row carries the target and the key together",
			entry: Entry{
				TraceID: "failed", Stage: "on_error", Model: "gpt-4o", Provider: "openai",
				APIKeyID: "key-a", ErrorMessage: "connection reset mid-stream",
			},
			want: "key-a",
		},
		{
			// Empty rather than absent: the gateway saw a request with no
			// credential, which is every request on a gateway without key auth.
			name:  "an unauthenticated request records no key",
			entry: Entry{TraceID: "anonymous", Stage: "after_request", Model: "gpt-4o", Provider: "openai"},
			want:  "",
		},
	}

	for _, tc := range cases {
		if err := w.Write(context.Background(), tc.entry); err != nil {
			t.Fatalf("write %s: %v", tc.name, err)
		}
	}

	listed, err := w.List(context.Background(), Query{Limit: 100})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	byTrace := make(map[string]Entry, len(listed.Data))
	for _, e := range listed.Data {
		byTrace[e.TraceID] = e
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := byTrace[tc.entry.TraceID]
			if !ok {
				t.Fatalf("row %q was not listed back", tc.entry.TraceID)
			}
			if got.APIKeyID != tc.want {
				t.Errorf("api_key_id = %q, want %q", got.APIKeyID, tc.want)
			}
			if got.Provider != tc.entry.Provider {
				t.Errorf("provider = %q, want %q", got.Provider, tc.entry.Provider)
			}
			if got.TotalTokens != tc.entry.TotalTokens {
				t.Errorf("total_tokens = %d, want %d", got.TotalTokens, tc.entry.TotalTokens)
			}
			switch {
			case tc.entry.CostUSD == nil && got.CostUSD != nil:
				t.Errorf("cost_usd = %v, want unpriced", *got.CostUSD)
			case tc.entry.CostUSD != nil && got.CostUSD == nil:
				t.Error("a recorded cost of zero came back as unpriced, which is a different claim")
			case tc.entry.CostUSD != nil && *got.CostUSD != *tc.entry.CostUSD:
				t.Errorf("cost_usd = %v, want %v", *got.CostUSD, *tc.entry.CostUSD)
			}
		})
	}
}
