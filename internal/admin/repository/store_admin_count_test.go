package repository

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/admin/model"
)

// TestCountAdminKeys pins the predicate the last-admin guard depends on. The
// memory store scans a Go slice while the SQL store matches the persisted JSON
// scopes with LIKE, so the two implementations must be proven to agree —
// especially on scopes that merely contain "admin" as a substring.
func TestCountAdminKeys(t *testing.T) {
	cases := []struct {
		name string
		seed func(t *testing.T, store Store)
		want int
	}{
		{
			name: "empty store",
			seed: func(_ *testing.T, _ Store) {},
			want: 0,
		},
		{
			name: "one admin key",
			seed: func(t *testing.T, store Store) { seedKey(t, store, "a", []string{model.ScopeAdmin}) },
			want: 1,
		},
		{
			name: "two admin keys",
			seed: func(t *testing.T, store Store) {
				seedKey(t, store, "a", []string{model.ScopeAdmin})
				seedKey(t, store, "b", []string{model.ScopeAdmin})
			},
			want: 2,
		},
		{
			name: "read-only keys are not counted",
			seed: func(t *testing.T, store Store) {
				seedKey(t, store, "a", []string{model.ScopeAdmin})
				seedKey(t, store, "b", []string{model.ScopeReadOnly})
				seedKey(t, store, "c", []string{model.ScopeReadOnly})
			},
			want: 1,
		},
		{
			name: "a multi-scope key counts once",
			seed: func(t *testing.T, store Store) {
				seedKey(t, store, "a", []string{model.ScopeReadOnly, model.ScopeAdmin})
			},
			want: 1,
		},
		{
			name: "revoked admin keys are not counted",
			seed: func(t *testing.T, store Store) {
				dead := seedKey(t, store, "dead", []string{model.ScopeAdmin})
				if err := store.Revoke(t.Context(), dead.ID); err != nil {
					t.Fatalf("revoke: %v", err)
				}
				seedKey(t, store, "live", []string{model.ScopeAdmin})
			},
			want: 1,
		},
		{
			// An expired key authenticates nothing, so it holds nobody's access
			// open: counting it lets the guard clear the last key that works.
			name: "expired admin keys are not counted",
			seed: func(t *testing.T, store Store) {
				expired := time.Now().UTC().Add(-time.Hour)
				seedKeyExpiring(t, store, "stale", []string{model.ScopeAdmin}, &expired)
				seedKey(t, store, "live", []string{model.ScopeAdmin})
			},
			want: 1,
		},
		{
			// The SQL predicate matches the quoted JSON element "admin"; a scope
			// that merely contains those letters must not satisfy the guard.
			name: "scopes containing admin as a substring are not counted",
			seed: func(t *testing.T, store Store) {
				seedKey(t, store, "a", []string{"superadmin"})
				seedKey(t, store, "b", []string{"admin_read"})
				seedKey(t, store, "c", []string{"administrator"})
			},
			want: 0,
		},
	}

	for _, impl := range guardStoreImpls() {
		t.Run(impl.name, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					store := impl.newStore(t)
					tc.seed(t, store)

					got, err := store.CountAdminKeys(t.Context())
					if err != nil {
						t.Fatalf("count admin keys: %v", err)
					}
					if got != tc.want {
						t.Fatalf("got %d admin keys, want %d", got, tc.want)
					}
				})
			}
		})
	}
}

func seedKey(t *testing.T, store Store, name string, scopes []string) *model.APIKey {
	t.Helper()
	return seedKeyExpiring(t, store, name, scopes, nil)
}

func seedKeyExpiring(t *testing.T, store Store, name string, scopes []string, expiresAt *time.Time) *model.APIKey {
	t.Helper()
	key, err := store.Create(t.Context(), name, scopes, expiresAt)
	if err != nil {
		t.Fatalf("create key %q: %v", name, err)
	}
	return key
}

// guardStoreImpls lists the Store implementations the admin-count assertion
// runs against. The SQL count is a scopes LIKE match rather than a Go slice
// scan, so every dialect must be proven to agree with the memory store.
func guardStoreImpls() []struct {
	name     string
	newStore func(t *testing.T) Store
} {
	return []struct {
		name     string
		newStore func(t *testing.T) Store
	}{
		{name: "memory", newStore: func(_ *testing.T) Store { return NewKeyStore() }},
		{name: "sqlite", newStore: func(t *testing.T) Store { t.Helper(); return newSQLiteTestStore(t) }},
		{name: "postgres", newStore: newPostgresGuardStore},
	}
}

// newPostgresGuardStore returns a Postgres-backed store, skipping when no DSN
// is configured. The SQL implementation is shared by both dialects, so SQLite
// already covers the query text; this proves the same query is valid Postgres.
func newPostgresGuardStore(t *testing.T) Store {
	t.Helper()
	dsn := os.Getenv("FERROGW_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set FERROGW_TEST_POSTGRES_DSN to run Postgres guard tests")
	}
	store, err := NewPostgresStore(t.Context(), dsn)
	if err != nil {
		t.Fatalf("new postgres store: %v", err)
	}
	if _, err := store.db.ExecContext(t.Context(), "DELETE FROM api_keys"); err != nil {
		t.Fatalf("reset api_keys: %v", err)
	}
	t.Cleanup(func() {
		_, _ = store.db.ExecContext(context.Background(), "DELETE FROM api_keys")
		_ = store.db.Close()
	})
	return store
}
