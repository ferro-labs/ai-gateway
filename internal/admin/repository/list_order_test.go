package repository

import (
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/admin/model"
)

// listOrderImpl is one Store plus the ability to backdate a stored key, so a
// listing order can be asserted against timestamps the test chose rather than
// against whatever the clock produced during three consecutive Create calls.
type listOrderImpl struct {
	name         string
	newStore     func(t *testing.T) Store
	setCreatedAt func(t *testing.T, store Store, id string, at time.Time)
}

func listOrderImpls() []listOrderImpl {
	return []listOrderImpl{
		{
			name:     "memory",
			newStore: func(_ *testing.T) Store { return NewKeyStore() },
			setCreatedAt: func(t *testing.T, store Store, id string, at time.Time) {
				t.Helper()
				s, ok := store.(*KeyStore)
				if !ok {
					t.Fatalf("store is %T, want *KeyStore", store)
				}
				s.mu.Lock()
				defer s.mu.Unlock()
				s.byID[id].apiKey.CreatedAt = at
			},
		},
		{
			name:     "sqlite",
			newStore: func(t *testing.T) Store { t.Helper(); return newSQLiteTestStore(t) },
			setCreatedAt: func(t *testing.T, store Store, id string, at time.Time) {
				t.Helper()
				s, ok := store.(*SQLStore)
				if !ok {
					t.Fatalf("store is %T, want *SQLStore", store)
				}
				if _, err := s.db.ExecContext(t.Context(), "UPDATE api_keys SET created_at = ? WHERE id = ?", at, id); err != nil {
					t.Fatalf("backdate %s: %v", id, err)
				}
			},
		},
	}
}

// GET /admin/keys serves whatever List returns, so an unordered List is a key
// table that reshuffles its rows under a poll that changed nothing. Both
// backends must answer newest-first, and must answer the same way: the memory
// store iterated a map (randomised per read) and the SQL store had no ORDER BY
// at all (left to the planner).
func TestKeyStoreListOrder_NewestFirst(t *testing.T) {
	base := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)

	for _, impl := range listOrderImpls() {
		t.Run(impl.name, func(t *testing.T) {
			store := impl.newStore(t)
			for i, name := range []string{"oldest", "middle", "newest"} {
				key, err := store.Create(t.Context(), name, []string{model.ScopeReadOnly}, nil)
				if err != nil {
					t.Fatalf("create %s: %v", name, err)
				}
				impl.setCreatedAt(t, store, key.ID, base.Add(time.Duration(i)*time.Hour))
			}

			// Repeated, because the defect this pins is non-determinism: one
			// read landing in the right order proves nothing.
			for attempt := range 5 {
				got := names(store.List(t.Context()))
				want := []string{"newest", "middle", "oldest"}
				if !equal(got, want) {
					t.Fatalf("attempt %d: got %v, want %v", attempt, got, want)
				}
			}
		})
	}
}

// Keys minted in the same clock tick — which a scripted rollout produces — must
// still come back in one fixed order. Without the id tiebreak the map iteration
// (memory) and the planner (SQL) each pick their own, so the same unchanged
// store lists them differently on consecutive reads.
func TestKeyStoreListOrder_TiebreaksOnID(t *testing.T) {
	sameInstant := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)

	for _, impl := range listOrderImpls() {
		t.Run(impl.name, func(t *testing.T) {
			store := impl.newStore(t)
			for _, name := range []string{"a", "b", "c", "d", "e"} {
				key, err := store.Create(t.Context(), name, []string{model.ScopeReadOnly}, nil)
				if err != nil {
					t.Fatalf("create %s: %v", name, err)
				}
				impl.setCreatedAt(t, store, key.ID, sameInstant)
			}

			first := ids(store.List(t.Context()))
			for i := 1; i < len(first); i++ {
				if first[i-1] <= first[i] {
					t.Fatalf("ids are not in descending order: %v", first)
				}
			}
			for attempt := range 5 {
				if got := ids(store.List(t.Context())); !equal(got, first) {
					t.Fatalf("attempt %d: order changed between reads: %v then %v", attempt, first, got)
				}
			}
		})
	}
}

// The last-admin guard asks CountAdminKeys whether removing a key would lock
// everyone out, so the two stores must answer with the same predicate rather
// than merely agreeing on the rows Revoke() happens to write.
// model.KeyIsUsable rejects a key with revoked_at set whatever active says; the
// SQL count checked active alone, so a row that reached the table any other way
// — a restore, a hand-repaired database, an operator UPDATE — was counted as an
// admin that could authenticate, and the guard would then clear the last key
// that actually works.
func TestCountAdminKeys_RevokedRowWithActiveStillTrue(t *testing.T) {
	store := newSQLiteTestStore(t)

	key, err := store.Create(t.Context(), "half-revoked", []string{model.ScopeAdmin}, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Revoke() writes revoked_at and active together; this writes only the
	// first, which is the state the memory store already reads as unusable.
	if _, err := store.db.ExecContext(t.Context(),
		"UPDATE api_keys SET revoked_at = ? WHERE id = ?", time.Now().UTC().Add(-time.Hour), key.ID); err != nil {
		t.Fatalf("mark revoked: %v", err)
	}

	if got, _ := store.Get(t.Context(), key.ID); !model.IsUsableAdmin(got) {
		// Guards the premise: the row must be one the shared predicate rejects.
		t.Log("row correctly reads as an unusable admin")
	} else {
		t.Fatal("premise broken: the seeded row still reads as a usable admin")
	}

	count, err := store.CountAdminKeys(t.Context())
	if err != nil {
		t.Fatalf("count admin keys: %v", err)
	}
	if count != 0 {
		t.Fatalf("got %d admin keys, want 0 — a revoked key authenticates nothing", count)
	}
}

func names(keys []*model.APIKey) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k.Name)
	}
	return out
}

func ids(keys []*model.APIKey) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k.ID)
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
