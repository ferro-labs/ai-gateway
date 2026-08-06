package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/admin/model"
	"github.com/ferro-labs/ai-gateway/internal/admin/repository"
	"github.com/go-chi/chi/v5"
)

// guardMasterKey is the MASTER_KEY value the guard routers accept. The master
// key is the only credential that can reach a store holding exactly one admin
// key without being that key itself, so the last-admin cases are driven through
// it.
const guardMasterKey = "fgw_guard_test_master_key"

// masterCaller mirrors the fabricated identity AuthMiddleware installs for the
// master key: an ID derived from the configured key value, admin scope, and no
// row in any store.
func masterCaller() *model.APIKey {
	id := masterCredentialID(guardMasterKey)
	return &model.APIKey{ID: id, Name: id, Key: guardMasterKey, Scopes: []string{model.ScopeAdmin}, Active: true}
}

// guardStoreImpls lists the Store implementations every guard assertion runs
// against. The guard reads the admin-key count from the store, and the SQL
// count is a scopes LIKE match rather than a Go slice scan, so the two must be
// proven to agree.
func guardStoreImpls() []struct {
	name     string
	newStore func(t *testing.T) repository.Store
} {
	return []struct {
		name     string
		newStore func(t *testing.T) repository.Store
	}{
		{name: "memory", newStore: func(_ *testing.T) repository.Store { return repository.NewKeyStore() }},
		{name: "sqlite", newStore: func(t *testing.T) repository.Store { t.Helper(); return newSQLiteTestStore(t) }},
		{name: "postgres", newStore: newPostgresGuardStore},
	}
}

// newPostgresGuardStore returns a Postgres-backed store, skipping when no DSN
// is configured. The SQL implementation is shared by both dialects, so SQLite
// already covers the query text; this proves the same query is valid Postgres.
func newPostgresGuardStore(t *testing.T) repository.Store {
	t.Helper()
	dsn := os.Getenv("FERROGW_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set FERROGW_TEST_POSTGRES_DSN to run Postgres guard tests")
	}
	store, err := repository.NewPostgresStore(t.Context(), dsn)
	if err != nil {
		t.Fatalf("new postgres store: %v", err)
	}
	// Start from an empty api_keys table. The store's internal *sql.DB is not
	// reachable from this package, so clear it through the exported API.
	for _, k := range store.List(t.Context()) {
		if err := store.Delete(t.Context(), k.ID); err != nil {
			t.Fatalf("reset api_keys: %v", err)
		}
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// newSQLiteTestStore returns a SQLite-backed key store on a throwaway temp-dir
// database. The backing file lives under t.TempDir(), so it is removed with the
// test; Close releases the connection.
func newSQLiteTestStore(t *testing.T) *repository.SQLStore {
	t.Helper()
	store, err := repository.NewSQLiteStore(t.Context(), filepath.Join(t.TempDir(), "keys.db"))
	if err != nil {
		t.Fatalf("new sqlite store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// newGuardRouter mounts the admin routes over store with the master key
// enabled.
func newGuardRouter(t *testing.T, store repository.Store) (*Handlers, chi.Router) {
	t.Helper()
	h := &Handlers{Keys: store}
	r := chi.NewRouter()
	r.Use(AuthMiddleware(store, guardMasterKey))
	r.Mount("/admin", h.Routes())
	return h, r
}

// errorCode extracts the machine-readable code from an admin error envelope.
func errorCode(t *testing.T, body []byte) string {
	t.Helper()
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode error envelope %q: %v", body, err)
	}
	return envelope.Error.Code
}

// guardCase is one request against a seeded store plus the response the guards
// must produce.
type guardCase struct {
	name string
	// arrange seeds the store and returns the credential the request
	// authenticates with plus the key id it targets.
	arrange    func(t *testing.T, h *Handlers) (caller *model.APIKey, targetID string)
	method     string
	path       func(id string) string
	body       string
	wantStatus int
	wantCode   string
}

// runGuardCases replays every case against every Store implementation: the
// guard reads its admin-key count from the store, so both must enforce it
// identically.
func runGuardCases(t *testing.T, cases []guardCase) {
	t.Helper()
	for _, impl := range guardStoreImpls() {
		t.Run(impl.name, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					h, router := newGuardRouter(t, impl.newStore(t))
					caller, targetID := tc.arrange(t, h)

					req := authedRequest(tc.method, tc.path(targetID), tc.body, caller)
					w := httptest.NewRecorder()
					router.ServeHTTP(w, req)

					if w.Code != tc.wantStatus {
						t.Fatalf("got status %d, want %d: %s", w.Code, tc.wantStatus, w.Body.String())
					}
					if tc.wantCode == "" {
						return
					}
					if got := errorCode(t, w.Body.Bytes()); got != tc.wantCode {
						t.Fatalf("got error code %q, want %q", got, tc.wantCode)
					}
				})
			}
		})
	}
}

// TestSelfMutationGuard covers the refusal to delete, revoke, or de-scope the
// credential the request authenticated with. Every case seeds a second admin
// key so the last-admin guard cannot be what produces the refusal.
func TestSelfMutationGuard(t *testing.T) {
	runGuardCases(t, []guardCase{
		{
			name: "self delete is refused",
			arrange: func(t *testing.T, h *Handlers) (*model.APIKey, string) {
				caller := createTestKey(t, h, "caller", []string{model.ScopeAdmin}, nil)
				createTestKey(t, h, "spare-admin", []string{model.ScopeAdmin}, nil)
				return caller, caller.ID
			},
			method:     http.MethodDelete,
			path:       func(id string) string { return "/admin/keys/" + id },
			wantStatus: http.StatusConflict,
			wantCode:   codeSelfMutation,
		},
		{
			name: "self revoke is refused",
			arrange: func(t *testing.T, h *Handlers) (*model.APIKey, string) {
				caller := createTestKey(t, h, "caller", []string{model.ScopeAdmin}, nil)
				createTestKey(t, h, "spare-admin", []string{model.ScopeAdmin}, nil)
				return caller, caller.ID
			},
			method:     http.MethodPost,
			path:       func(id string) string { return "/admin/keys/" + id + "/revoke" },
			wantStatus: http.StatusConflict,
			wantCode:   codeSelfMutation,
		},
		{
			name: "self de-scope is refused",
			arrange: func(t *testing.T, h *Handlers) (*model.APIKey, string) {
				caller := createTestKey(t, h, "caller", []string{model.ScopeAdmin}, nil)
				createTestKey(t, h, "spare-admin", []string{model.ScopeAdmin}, nil)
				return caller, caller.ID
			},
			method:     http.MethodPut,
			path:       func(id string) string { return "/admin/keys/" + id },
			body:       `{"scopes":["read_only"]}`,
			wantStatus: http.StatusConflict,
			wantCode:   codeSelfMutation,
		},
		{
			// An expiry in the past takes effect the moment the handler
			// returns, so it is a revoke by another name and must be refused
			// exactly as a revoke is. Without this the whole guard is
			// bypassable by sending a past timestamp.
			name: "self expiry in the past is refused",
			arrange: func(t *testing.T, h *Handlers) (*model.APIKey, string) {
				caller := createTestKey(t, h, "caller", []string{model.ScopeAdmin}, nil)
				createTestKey(t, h, "spare-admin", []string{model.ScopeAdmin}, nil)
				return caller, caller.ID
			},
			method:     http.MethodPut,
			path:       func(id string) string { return "/admin/keys/" + id },
			body:       `{"expires_at":"2020-01-01T00:00:00Z"}`,
			wantStatus: http.StatusConflict,
			wantCode:   codeSelfMutation,
		},
		{
			// A future expiry is a scheduled clock event no handler check can
			// prevent, so guarding it would buy nothing and break retiring a
			// key on a schedule.
			name: "self expiry in the future is allowed",
			arrange: func(t *testing.T, h *Handlers) (*model.APIKey, string) {
				caller := createTestKey(t, h, "caller", []string{model.ScopeAdmin}, nil)
				return caller, caller.ID
			},
			method:     http.MethodPut,
			path:       func(id string) string { return "/admin/keys/" + id },
			body:       `{"expires_at":"2099-01-01T00:00:00Z"}`,
			wantStatus: http.StatusOK,
		},
		{
			name: "self rename is allowed",
			arrange: func(t *testing.T, h *Handlers) (*model.APIKey, string) {
				caller := createTestKey(t, h, "caller", []string{model.ScopeAdmin}, nil)
				return caller, caller.ID
			},
			method:     http.MethodPut,
			path:       func(id string) string { return "/admin/keys/" + id },
			body:       `{"name":"renamed"}`,
			wantStatus: http.StatusOK,
		},
		{
			name: "self re-scope keeping admin is allowed",
			arrange: func(t *testing.T, h *Handlers) (*model.APIKey, string) {
				caller := createTestKey(t, h, "caller", []string{model.ScopeAdmin}, nil)
				return caller, caller.ID
			},
			method:     http.MethodPut,
			path:       func(id string) string { return "/admin/keys/" + id },
			body:       `{"scopes":["admin","read_only"]}`,
			wantStatus: http.StatusOK,
		},
		{
			// Rotation hands the new secret back in the response, so the caller
			// keeps a working credential and cannot lock itself out.
			name: "self rotate is allowed",
			arrange: func(t *testing.T, h *Handlers) (*model.APIKey, string) {
				caller := createTestKey(t, h, "caller", []string{model.ScopeAdmin}, nil)
				return caller, caller.ID
			},
			method:     http.MethodPost,
			path:       func(id string) string { return "/admin/keys/" + id + "/rotate" },
			wantStatus: http.StatusOK,
		},
		{
			// The master key's identity is fabricated and has no stored row, so
			// it is its own credential in the self-mutation sense.
			name: "master key cannot target its own fabricated id",
			arrange: func(t *testing.T, h *Handlers) (*model.APIKey, string) {
				createTestKey(t, h, "spare-admin", []string{model.ScopeAdmin}, nil)
				caller := masterCaller()
				return caller, caller.ID
			},
			method:     http.MethodDelete,
			path:       func(id string) string { return "/admin/keys/" + id },
			wantStatus: http.StatusConflict,
			wantCode:   codeSelfMutation,
		},
	})
}

// TestLastAdminGuard covers the refusal to remove the final key that can
// authenticate an admin request. The master key drives these cases because it
// is the only credential that can reach a store holding one admin key without
// being that key itself.
func TestLastAdminGuard(t *testing.T) {
	runGuardCases(t, []guardCase{
		{
			name: "deleting the last admin key is refused",
			arrange: func(t *testing.T, h *Handlers) (*model.APIKey, string) {
				only := createTestKey(t, h, "only-admin", []string{model.ScopeAdmin}, nil)
				return masterCaller(), only.ID
			},
			method:     http.MethodDelete,
			path:       func(id string) string { return "/admin/keys/" + id },
			wantStatus: http.StatusConflict,
			wantCode:   codeLastAdminKey,
		},
		{
			// Same bypass as the self case, one level out: expiring the only
			// admin key in the past empties the store of usable admins just as
			// deleting it would.
			name: "expiring the last admin key in the past is refused",
			arrange: func(t *testing.T, h *Handlers) (*model.APIKey, string) {
				only := createTestKey(t, h, "only-admin", []string{model.ScopeAdmin}, nil)
				return masterCaller(), only.ID
			},
			method:     http.MethodPut,
			path:       func(id string) string { return "/admin/keys/" + id },
			body:       `{"expires_at":"2020-01-01T00:00:00Z"}`,
			wantStatus: http.StatusConflict,
			wantCode:   codeLastAdminKey,
		},
		{
			name: "revoking the last admin key is refused",
			arrange: func(t *testing.T, h *Handlers) (*model.APIKey, string) {
				only := createTestKey(t, h, "only-admin", []string{model.ScopeAdmin}, nil)
				return masterCaller(), only.ID
			},
			method:     http.MethodPost,
			path:       func(id string) string { return "/admin/keys/" + id + "/revoke" },
			wantStatus: http.StatusConflict,
			wantCode:   codeLastAdminKey,
		},
		{
			name: "stripping admin from the last admin key is refused",
			arrange: func(t *testing.T, h *Handlers) (*model.APIKey, string) {
				only := createTestKey(t, h, "only-admin", []string{model.ScopeAdmin}, nil)
				return masterCaller(), only.ID
			},
			method:     http.MethodPut,
			path:       func(id string) string { return "/admin/keys/" + id },
			body:       `{"scopes":["read_only"]}`,
			wantStatus: http.StatusConflict,
			wantCode:   codeLastAdminKey,
		},
		{
			// A read-only key alongside the sole admin key must not be mistaken
			// for a second admin credential.
			name: "a read-only key does not satisfy the last-admin guard",
			arrange: func(t *testing.T, h *Handlers) (*model.APIKey, string) {
				only := createTestKey(t, h, "only-admin", []string{model.ScopeAdmin}, nil)
				createTestKey(t, h, "reader", []string{model.ScopeReadOnly}, nil)
				return masterCaller(), only.ID
			},
			method:     http.MethodDelete,
			path:       func(id string) string { return "/admin/keys/" + id },
			wantStatus: http.StatusConflict,
			wantCode:   codeLastAdminKey,
		},
		{
			// An already-revoked admin key cannot authenticate, so it must not
			// count toward the remaining admins.
			name: "a revoked admin key does not satisfy the last-admin guard",
			arrange: func(t *testing.T, h *Handlers) (*model.APIKey, string) {
				dead := createTestKey(t, h, "revoked-admin", []string{model.ScopeAdmin}, nil)
				if err := h.Keys.Revoke(t.Context(), dead.ID); err != nil {
					t.Fatalf("revoke seed key: %v", err)
				}
				live := createTestKey(t, h, "live-admin", []string{model.ScopeAdmin}, nil)
				return masterCaller(), live.ID
			},
			method:     http.MethodDelete,
			path:       func(id string) string { return "/admin/keys/" + id },
			wantStatus: http.StatusConflict,
			wantCode:   codeLastAdminKey,
		},
		{
			// An expired admin key cannot authenticate today, so it holds
			// nobody's access open and must not stand in for the live key the
			// request is about to remove.
			name: "an expired admin key does not satisfy the last-admin guard",
			arrange: func(t *testing.T, h *Handlers) (*model.APIKey, string) {
				expired := time.Now().UTC().Add(-time.Hour)
				createTestKey(t, h, "expired-admin", []string{model.ScopeAdmin}, &expired)
				live := createTestKey(t, h, "live-admin", []string{model.ScopeAdmin}, nil)
				return masterCaller(), live.ID
			},
			method:     http.MethodDelete,
			path:       func(id string) string { return "/admin/keys/" + id },
			wantStatus: http.StatusConflict,
			wantCode:   codeLastAdminKey,
		},
		{
			// The other side of the same rule: an expired key is not what the
			// guard is protecting, so clearing it away is always allowed.
			name: "deleting an expired admin key as the sole live admin succeeds",
			arrange: func(t *testing.T, h *Handlers) (*model.APIKey, string) {
				createTestKey(t, h, "caller", []string{model.ScopeAdmin}, nil)
				expired := time.Now().UTC().Add(-time.Hour)
				target := createTestKey(t, h, "expired-admin", []string{model.ScopeAdmin}, &expired)
				return masterCaller(), target.ID
			},
			method:     http.MethodDelete,
			path:       func(id string) string { return "/admin/keys/" + id },
			wantStatus: http.StatusNoContent,
		},
		{
			name: "deleting an admin key with another admin present succeeds",
			arrange: func(t *testing.T, h *Handlers) (*model.APIKey, string) {
				caller := createTestKey(t, h, "caller", []string{model.ScopeAdmin}, nil)
				target := createTestKey(t, h, "target", []string{model.ScopeAdmin}, nil)
				return caller, target.ID
			},
			method:     http.MethodDelete,
			path:       func(id string) string { return "/admin/keys/" + id },
			wantStatus: http.StatusNoContent,
		},
		{
			name: "revoking an admin key with another admin present succeeds",
			arrange: func(t *testing.T, h *Handlers) (*model.APIKey, string) {
				caller := createTestKey(t, h, "caller", []string{model.ScopeAdmin}, nil)
				target := createTestKey(t, h, "target", []string{model.ScopeAdmin}, nil)
				return caller, target.ID
			},
			method:     http.MethodPost,
			path:       func(id string) string { return "/admin/keys/" + id + "/revoke" },
			wantStatus: http.StatusOK,
		},
		{
			// The sole admin key may still delete a non-admin key: removing it
			// takes nobody's access away.
			name: "deleting a read-only key as the sole admin succeeds",
			arrange: func(t *testing.T, h *Handlers) (*model.APIKey, string) {
				caller := createTestKey(t, h, "only-admin", []string{model.ScopeAdmin}, nil)
				target := createTestKey(t, h, "reader", []string{model.ScopeReadOnly}, nil)
				return caller, target.ID
			},
			method:     http.MethodDelete,
			path:       func(id string) string { return "/admin/keys/" + id },
			wantStatus: http.StatusNoContent,
		},
		{
			name: "master key may delete a stored key when another admin remains",
			arrange: func(t *testing.T, h *Handlers) (*model.APIKey, string) {
				createTestKey(t, h, "spare-admin", []string{model.ScopeAdmin}, nil)
				target := createTestKey(t, h, "target", []string{model.ScopeAdmin}, nil)
				return masterCaller(), target.ID
			},
			method:     http.MethodDelete,
			path:       func(id string) string { return "/admin/keys/" + id },
			wantStatus: http.StatusNoContent,
		},
		{
			name: "a missing key still reports not found",
			arrange: func(t *testing.T, h *Handlers) (*model.APIKey, string) {
				caller := createTestKey(t, h, "caller", []string{model.ScopeAdmin}, nil)
				return caller, "no-such-key"
			},
			method:     http.MethodDelete,
			path:       func(id string) string { return "/admin/keys/" + id },
			wantStatus: http.StatusNotFound,
		},
	})
}

// TestKeyMutationGuards_RefusalLeavesKeyUsable proves a refusal is not a
// partial application: the credential the guard protected must still
// authenticate afterwards. Without this, a guard that returned 409 after
// already writing would be indistinguishable from one that never wrote.
func TestKeyMutationGuards_RefusalLeavesKeyUsable(t *testing.T) {
	for _, impl := range guardStoreImpls() {
		t.Run(impl.name, func(t *testing.T) {
			store := impl.newStore(t)
			h, router := newGuardRouter(t, store)
			only := createTestKey(t, h, "only-admin", []string{model.ScopeAdmin}, nil)

			req := authedRequest(http.MethodDelete, "/admin/keys/"+only.ID, "", masterCaller())
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != http.StatusConflict {
				t.Fatalf("got status %d, want 409: %s", w.Code, w.Body.String())
			}

			validated, ok := store.ValidateKey(t.Context(), only.Key)
			if !ok {
				t.Fatal("the protected key must still authenticate after a refusal")
			}
			if !model.HasAdminScope(validated.Scopes) {
				t.Fatalf("the protected key must keep its admin scope, got %v", validated.Scopes)
			}
		})
	}
}
