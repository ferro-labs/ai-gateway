package repository

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/sqldb"

	"github.com/ferro-labs/ai-gateway/internal/admin/model"
)

// sqlSessionStoreCase pairs a SQL-backed SessionStore with the *sql.DB behind
// it and its dialect, so a test can reach around the SessionStore interface
// to manipulate a row directly (e.g. backdating last_seen_at to exercise the
// idle bound) using the correct placeholder form for the dialect.
type sqlSessionStoreCase struct {
	store   SessionStore
	db      *sql.DB
	dialect sqldb.Dialect
}

// newTestSQLSessionStores returns every SQL-backed SessionStore
// implementation available in this environment (sqlite always, postgres when
// FERROGW_TEST_POSTGRES_DSN is set), each paired with its underlying *sql.DB.
//
// Memory is excluded on purpose: it has no *sql.DB to pair with, and
// TestMemorySessionStoreRejectsIdle already covers the equivalent case by
// backdating through its own map.
func newTestSQLSessionStores(t *testing.T) map[string]sqlSessionStoreCase {
	t.Helper()
	stores := make(map[string]sqlSessionStoreCase)

	dsn := filepath.Join(t.TempDir(), "sessions.db")
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	sqliteStore, err := NewSQLSessionStore(t.Context(), db, sqldb.SQLite)
	if err != nil {
		t.Fatalf("NewSQLSessionStore sqlite: %v", err)
	}
	stores["sqlite"] = sqlSessionStoreCase{store: sqliteStore, db: db, dialect: sqldb.SQLite}

	if pgDSN := os.Getenv("FERROGW_TEST_POSTGRES_DSN"); pgDSN != "" {
		pgDB, err := sql.Open("postgres", pgDSN)
		if err != nil {
			t.Fatalf("open postgres: %v", err)
		}
		t.Cleanup(func() { _ = pgDB.Close() })
		pgStore, err := NewSQLSessionStore(t.Context(), pgDB, sqldb.Postgres)
		if err != nil {
			t.Fatalf("NewSQLSessionStore postgres: %v", err)
		}
		stores["postgres"] = sqlSessionStoreCase{store: pgStore, db: pgDB, dialect: sqldb.Postgres}
	}
	return stores
}

// newTestSessionStores returns every SessionStore implementation available in
// this environment, so one table of assertions covers all of them.
func newTestSessionStores(t *testing.T) map[string]SessionStore {
	t.Helper()
	stores := map[string]SessionStore{"memory": NewSessionStore()}
	for name, c := range newTestSQLSessionStores(t) {
		stores[name] = c.store
	}
	return stores
}

func TestSessionStoreConformance(t *testing.T) {
	for name, store := range newTestSessionStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()

			// A subject unique to this run, because a shared Postgres database
			// carries rows from every other test in this package. The listing is
			// read whole and this run's rows are picked out of it, the same way
			// TestSessionListOrder_StableNewestFirst does, so the count holds
			// without emptying a table another test owns.
			subject := fmt.Sprintf("conformance-%s-%d", name, time.Now().UnixNano())

			sess, token, err := store.CreateSession(ctx, subject, "cred-123", []string{model.ScopeAdmin}, DefaultSessionTTL)
			if err != nil {
				t.Fatalf("CreateSession: %v", err)
			}

			got, ok := store.ValidateSession(ctx, token)
			if !ok {
				t.Fatal("fresh token rejected")
			}
			if got.Subject != subject || got.CredentialID != "cred-123" || len(got.Scopes) != 1 || got.Scopes[0] != model.ScopeAdmin {
				t.Fatalf("round-trip lost identity: %+v", got)
			}

			if own := listOwn(t, store, subject); len(own) != 1 {
				t.Fatalf("ListSessions returned %d rows for %s, want 1", len(own), subject)
			}

			if err := store.DeleteSession(ctx, sess.ID); err != nil {
				t.Fatalf("DeleteSession: %v", err)
			}
			if _, ok := store.ValidateSession(ctx, token); ok {
				t.Fatal("deleted session still validates")
			}
			if err := store.DeleteSession(ctx, sess.ID); err == nil {
				t.Fatal("deleting a missing session returned nil error")
			}
		})
	}
}

func TestSessionStoreRejectsExpiredConformance(t *testing.T) {
	for name, store := range newTestSessionStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			_, token, err := store.CreateSession(ctx, "s", "s", []string{model.ScopeAdmin}, -time.Second)
			if err != nil {
				t.Fatalf("CreateSession: %v", err)
			}
			if _, ok := store.ValidateSession(ctx, token); ok {
				t.Fatal("expired session validated")
			}
		})
	}
}

// TestSessionStoreRejectsIdleConformance backdates last_seen_at directly in
// the database (rather than through the store's own API, which has no way to
// do so), exercising the UPDATE -> SELECT -> sql.NullTime -> scanSession
// round-trip at a point where a stale value actually flips the outcome.
// TestSessionStoreConformance's fresh-token round-trip cannot reach this
// branch: the value it scans is only milliseconds old.
//
// Memory is deliberately excluded here (see newTestSQLSessionStores):
// TestMemorySessionStoreRejectsIdle in sessions_test.go already covers it by
// backdating through the map directly.
func TestSessionStoreRejectsIdleConformance(t *testing.T) {
	for name, c := range newTestSQLSessionStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			sess, token, err := c.store.CreateSession(ctx, "master-key", "master-key", []string{model.ScopeAdmin}, DefaultSessionTTL)
			if err != nil {
				t.Fatalf("CreateSession: %v", err)
			}

			old := time.Now().UTC().Add(-2 * DefaultSessionIdleTTL)
			update := sqldb.Bind(c.dialect, "UPDATE sessions SET last_seen_at = ? WHERE id = ?")
			if _, err := c.db.ExecContext(ctx, update, old, sess.ID); err != nil {
				t.Fatalf("backdate last_seen_at: %v", err)
			}

			if _, ok := c.store.ValidateSession(ctx, token); ok {
				t.Fatal("an idle-expired session validated")
			}
		})
	}
}

func TestSessionStoreDeleteAllConformance(t *testing.T) {
	for name, store := range newTestSessionStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			_, a, _ := store.CreateSession(ctx, "one", "one", []string{model.ScopeAdmin}, DefaultSessionTTL)
			_, _, _ = store.CreateSession(ctx, "two", "two", []string{model.ScopeAdmin}, DefaultSessionTTL)

			n, err := store.DeleteAllSessions(ctx)
			if err != nil {
				t.Fatalf("DeleteAllSessions: %v", err)
			}
			if n < 2 {
				t.Fatalf("deleted %d, want at least 2", n)
			}
			if _, ok := store.ValidateSession(ctx, a); ok {
				t.Fatal("session survived DeleteAllSessions")
			}
		})
	}
}
