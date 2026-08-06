//go:build integration
// +build integration

package integration

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/requestlog"

	_ "github.com/lib/pq"
)

// isolatedSchemaDSN gives one test its own Postgres schema, so a run that
// creates request_logs from nothing does not collide with the other tests
// sharing this container. The schema is dropped when the test ends.
func isolatedSchemaDSN(t *testing.T, name string) string {
	t.Helper()

	admin, err := sql.Open("postgres", testDSN)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close() })

	ctx := t.Context()
	if _, err := admin.ExecContext(ctx, "DROP SCHEMA IF EXISTS "+name+" CASCADE"); err != nil {
		t.Fatalf("drop schema %s: %v", name, err)
	}
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+name); err != nil {
		t.Fatalf("create schema %s: %v", name, err)
	}
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), "DROP SCHEMA IF EXISTS "+name+" CASCADE")
	})

	separator := "?"
	if strings.Contains(testDSN, "?") {
		separator = "&"
	}
	return testDSN + separator + "search_path=" + name
}

// TestConcurrentColdStart is the regression guard for the migration deadlock:
// several instances starting at once against one fresh database.
//
// It used to wedge permanently. A blocking pg_advisory_lock holds a snapshot for
// the whole wait, and CREATE INDEX CONCURRENTLY inside the lock then waited on
// the waiter's virtualxid — a cycle Postgres's deadlock detector does not look
// for. The budget here is what makes the test meaningful: a wedged run does not
// fail slowly, it never finishes.
func TestConcurrentColdStart(t *testing.T) {
	dsn := isolatedSchemaDSN(t, "s8_cold_start")

	const instances = 4
	// Generous against the work (four steps on empty tables) and far under the
	// runner's own two-minute lock budget, so a failure here means wedged, not
	// slow.
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	errs := make(chan error, instances)
	var wg sync.WaitGroup
	for range instances {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w, err := requestlog.NewPostgresWriter(ctx, dsn)
			if w != nil {
				_ = w.Close()
			}
			errs <- err
		}()
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(45 * time.Second):
		t.Fatal("concurrent cold start never completed: the migration lock is wedged")
	}
	close(errs)

	for err := range errs {
		if err != nil {
			t.Errorf("instance failed to start: %v", err)
		}
	}
}

// TestCreatedAtIndexOnPopulatedTable covers the other half of the index
// decision. The step that builds the index concurrently only ever sees a
// populated table when it replays against a database whose ledger does not have
// it — a restore without the ledger, or a database predating the runner. This
// stages exactly that and asserts a valid index comes out of it.
func TestCreatedAtIndexOnPopulatedTable(t *testing.T) {
	dsn := isolatedSchemaDSN(t, "s8_populated")
	ctx := t.Context()

	w, err := requestlog.NewPostgresWriter(ctx, dsn)
	if err != nil {
		t.Fatalf("first start: %v", err)
	}
	for i := range 3 {
		if err := w.Write(ctx, requestlog.Entry{
			TraceID:   "seed-" + string(rune('a'+i)),
			Stage:     "after_request",
			Model:     "gpt-4o-mini",
			Provider:  "openai",
			CreatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("seed row %d: %v", i, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()

	// Put the schema back where a restore-without-ledger leaves it: rows
	// present, index gone, the index step unrecorded.
	if _, err := db.ExecContext(ctx, "DROP INDEX idx_request_logs_created_at"); err != nil {
		t.Fatalf("drop index: %v", err)
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM request_log_schema_migrations WHERE version = 2"); err != nil {
		t.Fatalf("unrecord index migration: %v", err)
	}

	again, err := requestlog.NewPostgresWriter(ctx, dsn)
	if err != nil {
		t.Fatalf("second start: %v", err)
	}
	defer func() { _ = again.Close() }()

	var valid bool
	err = db.QueryRowContext(ctx,
		"SELECT indisvalid FROM pg_index WHERE indexrelid = to_regclass('idx_request_logs_created_at')").Scan(&valid)
	if err != nil {
		t.Fatalf("probe rebuilt index: %v", err)
	}
	if !valid {
		t.Fatal("index rebuilt on a populated table is not valid")
	}

	var recorded int
	if err := db.QueryRowContext(ctx,
		"SELECT count(*) FROM request_log_schema_migrations WHERE version = 2").Scan(&recorded); err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if recorded != 1 {
		t.Fatalf("index migration recorded %d times, want 1", recorded)
	}
}
