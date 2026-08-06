package repository

import (
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/admin/model"
	"github.com/ferro-labs/ai-gateway/internal/sqldb"
)

// sweepCase is one row in the grid the sweep and isLive must agree on. Offsets
// are relative to a single "now" captured by the test.
type sweepCase struct {
	name       string
	expiresIn  time.Duration
	createdAgo time.Duration
	lastSeen   *time.Duration // nil means the row has never been seen
	wantLive   bool
}

func ago(d time.Duration) *time.Duration { return &d }

// sweepCases covers both bounds, both sides of each, and the exact boundary —
// which is where a SQL restatement of a Go comparison most easily drifts.
func sweepCases() []sweepCase {
	return []sweepCase{
		{name: "live", expiresIn: time.Hour, createdAgo: 2 * time.Hour, lastSeen: ago(time.Minute), wantLive: true},
		{name: "past absolute bound", expiresIn: -time.Minute, createdAgo: 25 * time.Hour, lastSeen: ago(time.Minute)},
		{name: "at absolute bound", expiresIn: 0, createdAgo: 24 * time.Hour, lastSeen: ago(time.Minute)},
		{name: "past idle bound", expiresIn: time.Hour, createdAgo: 3 * time.Hour, lastSeen: ago(61 * time.Minute)},
		{name: "at idle bound", expiresIn: time.Hour, createdAgo: 3 * time.Hour, lastSeen: ago(DefaultSessionIdleTTL)},
		{name: "inside idle bound", expiresIn: time.Hour, createdAgo: 3 * time.Hour, lastSeen: ago(59 * time.Minute), wantLive: true},
		{name: "both bounds passed", expiresIn: -time.Hour, createdAgo: 26 * time.Hour, lastSeen: ago(2 * time.Hour)},
		{name: "never seen, fresh", expiresIn: time.Hour, createdAgo: time.Minute, wantLive: true},
		{name: "never seen, idle out", expiresIn: time.Hour, createdAgo: 61 * time.Minute},
	}
}

func (c sweepCase) session(now time.Time) *model.Session {
	s := &model.Session{
		ID:           c.name,
		Subject:      "op",
		CredentialID: "cred",
		Scopes:       []string{"admin"},
		CreatedAt:    now.Add(-c.createdAgo),
		ExpiresAt:    now.Add(c.expiresIn),
	}
	if c.lastSeen != nil {
		seen := now.Add(-*c.lastSeen)
		s.LastSeenAt = &seen
	}
	return s
}

// TestIsLiveGrid pins the Go definition the sweeps are restated from, so a
// change to isLive that the SQL does not follow fails here first.
func TestIsLiveGrid(t *testing.T) {
	now := time.Now().UTC()
	for _, c := range sweepCases() {
		if got := isLive(c.session(now), now); got != c.wantLive {
			t.Errorf("%s: isLive = %v, want %v", c.name, got, c.wantLive)
		}
	}
}

// TestSweepMatchesIsLive is the drift guard sessions.go's isLive comment names:
// the DELETE predicate in sessions_sql.go must remove exactly the rows isLive
// rejects, on every SQL dialect available in this environment.
func TestSweepMatchesIsLive(t *testing.T) {
	now := time.Now().UTC()

	for name, tc := range newTestSQLSessionStores(t) {
		t.Run(name, func(t *testing.T) {
			store, ok := tc.store.(*SQLSessionStore)
			if !ok {
				t.Fatalf("expected *SQLSessionStore, got %T", tc.store)
			}
			insert := sqldb.Bind(tc.dialect,
				"INSERT INTO sessions(id, token_hash, subject, credential_id, scopes, created_at, last_seen_at, expires_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?)")
			for i, c := range sweepCases() {
				s := c.session(now)
				var lastSeen any
				if s.LastSeenAt != nil {
					lastSeen = *s.LastSeenAt
				}
				if _, err := tc.db.ExecContext(t.Context(), insert,
					s.ID, "hash-"+s.ID, s.Subject, s.CredentialID, `["admin"]`, s.CreatedAt, lastSeen, s.ExpiresAt); err != nil {
					t.Fatalf("insert case %d (%s): %v", i, c.name, err)
				}
			}

			store.sweepExpiredSessions(t.Context(), now)

			survivors := make(map[string]bool)
			rows, err := tc.db.QueryContext(t.Context(), "SELECT id FROM sessions")
			if err != nil {
				t.Fatalf("read survivors: %v", err)
			}
			defer func() { _ = rows.Close() }()
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					t.Fatalf("scan: %v", err)
				}
				survivors[id] = true
			}
			if err := rows.Err(); err != nil {
				t.Fatalf("iterate: %v", err)
			}

			for _, c := range sweepCases() {
				if survivors[c.name] != c.wantLive {
					t.Errorf("%s: survived sweep = %v, isLive = %v", c.name, survivors[c.name], c.wantLive)
				}
			}
		})
	}
}

// TestSweepRunsOnCreate covers the reclaim path itself: a dead row is gone once
// the next sign-in happens, which is the only thing that grows the table.
func TestSweepRunsOnCreate(t *testing.T) {
	for name, tc := range newTestSQLSessionStores(t) {
		t.Run(name, func(t *testing.T) {
			now := time.Now().UTC()
			insert := sqldb.Bind(tc.dialect,
				"INSERT INTO sessions(id, token_hash, subject, credential_id, scopes, created_at, expires_at) VALUES(?, ?, ?, ?, ?, ?, ?)")
			if _, err := tc.db.ExecContext(t.Context(), insert,
				"dead", "hash-dead", "op", "cred", `["admin"]`, now.Add(-26*time.Hour), now.Add(-2*time.Hour)); err != nil {
				t.Fatalf("seed expired session: %v", err)
			}

			if _, _, err := tc.store.CreateSession(t.Context(), "op", "cred", []string{"admin"}, DefaultSessionTTL); err != nil {
				t.Fatalf("create session: %v", err)
			}

			var remaining int
			q := sqldb.Bind(tc.dialect, "SELECT count(*) FROM sessions WHERE id = ?")
			if err := tc.db.QueryRowContext(t.Context(), q, "dead").Scan(&remaining); err != nil {
				t.Fatalf("count: %v", err)
			}
			if remaining != 0 {
				t.Errorf("expired session survived a sign-in: %d rows", remaining)
			}
		})
	}
}

// TestValidateThrottlesLastSeen pins both directions of the write throttle: no
// write while the stored value is fresher than the resolution, one write once it
// is not.
func TestValidateThrottlesLastSeen(t *testing.T) {
	for name, tc := range newTestSQLSessionStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			sess, token, err := tc.store.CreateSession(ctx, "op", "cred", []string{"admin"}, DefaultSessionTTL)
			if err != nil {
				t.Fatalf("create session: %v", err)
			}

			if _, ok := tc.store.ValidateSession(ctx, token); !ok {
				t.Fatal("fresh session did not validate")
			}
			if got := storedLastSeen(t, tc, sess.ID); got != nil {
				t.Errorf("last_seen_at written for a session created moments ago: %v", got)
			}

			// Age the idle clock past the resolution without passing either bound.
			backdate := sqldb.Bind(tc.dialect, "UPDATE sessions SET created_at = ? WHERE id = ?")
			if _, err := tc.db.ExecContext(ctx, backdate, time.Now().UTC().Add(-5*time.Minute), sess.ID); err != nil {
				t.Fatalf("backdate: %v", err)
			}

			if _, ok := tc.store.ValidateSession(ctx, token); !ok {
				t.Fatal("session did not validate after backdating")
			}
			written := storedLastSeen(t, tc, sess.ID)
			if written == nil {
				t.Fatal("last_seen_at not written once the stored value went stale")
			}

			// A second validation inside the resolution must not write again.
			if _, ok := tc.store.ValidateSession(ctx, token); !ok {
				t.Fatal("session did not validate on the second call")
			}
			again := storedLastSeen(t, tc, sess.ID)
			if again == nil || !again.Equal(*written) {
				t.Errorf("last_seen_at rewritten inside the resolution: %v then %v", written, again)
			}
		})
	}
}

func storedLastSeen(t *testing.T, tc sqlSessionStoreCase, id string) *time.Time {
	t.Helper()
	q := sqldb.Bind(tc.dialect, "SELECT last_seen_at FROM sessions WHERE id = ?")
	var raw *time.Time
	if err := tc.db.QueryRowContext(t.Context(), q, id).Scan(&raw); err != nil {
		t.Fatalf("read last_seen_at: %v", err)
	}
	if raw == nil {
		return nil
	}
	utc := raw.UTC()
	return &utc
}

// TestMemorySessionStoreSweepsOnCreate covers the same reclaim for the default
// deployment's store, which keeps sessions in maps rather than a table.
func TestMemorySessionStoreSweepsOnCreate(t *testing.T) {
	store := NewSessionStore()
	ctx := t.Context()

	_, deadToken, err := store.CreateSession(ctx, "op", "cred", []string{"admin"}, time.Millisecond)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	time.Sleep(5 * time.Millisecond)

	if _, ok := store.ValidateSession(ctx, deadToken); ok {
		t.Fatal("expired session validated")
	}
	if len(store.byID) != 1 {
		t.Fatalf("expected the dead row still present before a sign-in, got %d", len(store.byID))
	}

	if _, _, err := store.CreateSession(ctx, "op", "cred", []string{"admin"}, DefaultSessionTTL); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if len(store.byID) != 1 || len(store.byHash) != 1 {
		t.Errorf("sweep left %d sessions and %d hashes, want 1 and 1", len(store.byID), len(store.byHash))
	}
}

// TestMemorySessionStoreThrottlesLastSeen mirrors the SQL throttle so the two
// backends cannot drift into different idle semantics.
func TestMemorySessionStoreThrottlesLastSeen(t *testing.T) {
	store := NewSessionStore()
	ctx := t.Context()

	sess, token, err := store.CreateSession(ctx, "op", "cred", []string{"admin"}, DefaultSessionTTL)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, ok := store.ValidateSession(ctx, token); !ok {
		t.Fatal("fresh session did not validate")
	}
	if store.byID[sess.ID].LastSeenAt != nil {
		t.Error("last_seen written for a session created moments ago")
	}

	store.byID[sess.ID].CreatedAt = time.Now().UTC().Add(-5 * time.Minute)
	if _, ok := store.ValidateSession(ctx, token); !ok {
		t.Fatal("session did not validate after backdating")
	}
	first := store.byID[sess.ID].LastSeenAt
	if first == nil {
		t.Fatal("last_seen not written once the stored value went stale")
	}
	if _, ok := store.ValidateSession(ctx, token); !ok {
		t.Fatal("session did not validate on the second call")
	}
	if got := store.byID[sess.ID].LastSeenAt; got == nil || !got.Equal(*first) {
		t.Errorf("last_seen rewritten inside the resolution: %v then %v", first, got)
	}
}
