package repository

import (
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/admin/model"
	"github.com/ferro-labs/ai-gateway/internal/sqldb"
)

// GET /admin/sessions serves ListSessions verbatim, and the dashboard polls it,
// so an unordered listing reshuffles the rows an operator is reading. The memory
// store iterated a map — randomised on every read — while the SQL store ordered
// by created_at alone, which leaves sessions minted in the same clock tick (two
// tabs signing in together) to the planner.
func TestSessionListOrder_StableNewestFirst(t *testing.T) {
	sameInstant := time.Now().UTC().Add(-time.Minute)

	type impl struct {
		name         string
		store        SessionStore
		setCreatedAt func(t *testing.T, id string, at time.Time)
	}

	memory := NewSessionStore()
	sqlStores := newTestSQLSessionStores(t)
	impls := make([]impl, 0, len(sqlStores)+1)
	impls = append(impls, impl{
		name:  "memory",
		store: memory,
		setCreatedAt: func(t *testing.T, id string, at time.Time) {
			t.Helper()
			memory.mu.Lock()
			defer memory.mu.Unlock()
			memory.byID[id].CreatedAt = at
			// LastSeenAt drives the idle bound, so it moves with CreatedAt or a
			// backdated session simply disappears from the listing.
			seen := time.Now().UTC()
			memory.byID[id].LastSeenAt = &seen
		},
	})
	for name, sqlCase := range sqlStores {
		setCreatedAt := func(t *testing.T, id string, at time.Time) {
			t.Helper()
			query := sqldb.Bind(sqlCase.dialect, "UPDATE sessions SET created_at = ?, last_seen_at = ? WHERE id = ?")
			if _, err := sqlCase.db.ExecContext(t.Context(), query, at, time.Now().UTC(), id); err != nil {
				t.Fatalf("backdate %s: %v", id, err)
			}
		}
		impls = append(impls, impl{name: name, store: sqlCase.store, setCreatedAt: setCreatedAt})
	}

	for _, tc := range impls {
		t.Run(tc.name, func(t *testing.T) {
			// A named subject, because a shared Postgres database carries rows
			// from other tests. The listing is read whole and this run's rows are
			// picked out of it: a subsequence of a sorted list is still sorted,
			// so the assertion holds without emptying a table another test owns.
			subject := "order-probe-" + tc.name
			for range 5 {
				sess, _, err := tc.store.CreateSession(t.Context(), subject, "key-1", []string{model.ScopeAdmin}, time.Hour)
				if err != nil {
					t.Fatalf("create session: %v", err)
				}
				tc.setCreatedAt(t, sess.ID, sameInstant)
			}

			first := listOwn(t, tc.store, subject)
			if len(first) != 5 {
				t.Fatalf("got %d sessions, want 5", len(first))
			}
			for i := 1; i < len(first); i++ {
				if first[i-1] <= first[i] {
					t.Fatalf("ids are not in descending order: %v", first)
				}
			}
			// Repeated, because the defect is non-determinism: one correct read
			// proves nothing.
			for attempt := range 5 {
				if again := listOwn(t, tc.store, subject); !equal(again, first) {
					t.Fatalf("attempt %d: order changed between reads: %v then %v", attempt, first, again)
				}
			}
		})
	}
}

// listOwn returns the ids of subject's sessions, in listing order.
func listOwn(t *testing.T, store SessionStore, subject string) []string {
	t.Helper()
	sessions, err := store.ListSessions(t.Context())
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	out := make([]string, 0, len(sessions))
	for _, s := range sessions {
		if s.Subject == subject {
			out = append(out, s.ID)
		}
	}
	return out
}
