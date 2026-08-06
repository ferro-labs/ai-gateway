package handlers

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/admin/model"
	"github.com/ferro-labs/ai-gateway/internal/admin/repository"
	"github.com/ferro-labs/ai-gateway/internal/sqldb"
	"github.com/go-chi/chi/v5"
)

// newTestSessionStores returns one SessionStore per backend available in this
// environment: always memory and SQLite, plus Postgres when
// FERROGW_TEST_POSTGRES_DSN is set. Session conformance and handler tests run
// against every entry so the backends cannot drift apart.
func newTestSessionStores(t *testing.T) map[string]repository.SessionStore {
	t.Helper()
	stores := map[string]repository.SessionStore{"memory": repository.NewSessionStore()}

	dsn := filepath.Join(t.TempDir(), "sessions.db")
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	sqliteStore, err := repository.NewSQLSessionStore(t.Context(), db, sqldb.SQLite)
	if err != nil {
		t.Fatalf("NewSQLSessionStore sqlite: %v", err)
	}
	stores["sqlite"] = sqliteStore

	if pgDSN := os.Getenv("FERROGW_TEST_POSTGRES_DSN"); pgDSN != "" {
		pgDB, err := sql.Open("postgres", pgDSN)
		if err != nil {
			t.Fatalf("open postgres: %v", err)
		}
		t.Cleanup(func() { _ = pgDB.Close() })
		pgStore, err := repository.NewSQLSessionStore(t.Context(), pgDB, sqldb.Postgres)
		if err != nil {
			t.Fatalf("NewSQLSessionStore postgres: %v", err)
		}
		stores["postgres"] = pgStore
	}
	return stores
}

func TestSessionExchangeAndLogout(t *testing.T) {
	h, r := setupTestRouterWithSessions()
	adminKey := createAdminKey(t, h)

	// Exchange the API key for a session.
	req := authedRequest(http.MethodPost, "/admin/session", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("exchange: status = %d, want 201: %s", w.Code, w.Body.String())
	}

	var body struct {
		Token   string   `json:"token"`
		Scopes  []string `json:"scopes"`
		Subject string   `json:"subject"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.HasPrefix(body.Token, model.SessionTokenPrefix) {
		t.Fatalf("token %q lacks the session prefix", body.Token)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("session response is cacheable")
	}

	// The session token authenticates a normal admin read.
	listReq := tokenRequest(http.MethodGet, "/admin/keys", "", body.Token)
	listW := httptest.NewRecorder()
	r.ServeHTTP(listW, listReq)
	if listW.Code != http.StatusOK {
		t.Fatalf("session-authenticated read: status = %d, want 200", listW.Code)
	}

	// An admin session must retain admin scope end-to-end. A stub that
	// hardcoded read_only scope for every session would still pass every
	// assertion above without this one.
	createReq := tokenRequest(http.MethodPost, "/admin/keys", `{"name":"from-admin-session"}`, body.Token)
	createW := httptest.NewRecorder()
	r.ServeHTTP(createW, createReq)
	if createW.Code != http.StatusCreated {
		t.Fatalf("admin session creating a key: status = %d, want 201: %s", createW.Code, createW.Body.String())
	}

	// Logout invalidates it server-side — ASVS 5.0 7.4.1.
	outReq := tokenRequest(http.MethodDelete, "/admin/session", "", body.Token)
	outW := httptest.NewRecorder()
	r.ServeHTTP(outW, outReq)
	if outW.Code != http.StatusNoContent {
		t.Fatalf("logout: status = %d, want 204: %s", outW.Code, outW.Body.String())
	}

	afterReq := tokenRequest(http.MethodGet, "/admin/keys", "", body.Token)
	afterW := httptest.NewRecorder()
	r.ServeHTTP(afterW, afterReq)
	if afterW.Code != http.StatusUnauthorized {
		t.Fatalf("after logout: status = %d, want 401", afterW.Code)
	}
}

func TestSessionExchangeRejectsBadCredential(t *testing.T) {
	_, r := setupTestRouterWithSessions()

	req := tokenRequest(http.MethodPost, "/admin/session", "", "not-a-real-key")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if strings.Contains(w.Body.String(), model.SessionTokenPrefix) {
		t.Fatal("a rejected exchange returned a token")
	}
}

func TestSessionInheritsCredentialScopes(t *testing.T) {
	h, r := setupTestRouterWithSessions()
	readKey := createReadOnlyKey(t, h)

	req := authedRequest(http.MethodPost, "/admin/session", "", readKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("exchange: status = %d, want 201", w.Code)
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// A read-only session must not be able to create a key.
	createReq := tokenRequest(http.MethodPost, "/admin/keys", `{"name":"x"}`, body.Token)
	createW := httptest.NewRecorder()
	r.ServeHTTP(createW, createReq)
	if createW.Code != http.StatusForbidden {
		t.Fatalf("read-only session creating a key: status = %d, want 403", createW.Code)
	}
}

func TestRevokeAllSessions(t *testing.T) {
	h, r := setupTestRouterWithSessions()
	adminKey := createAdminKey(t, h)

	exchange := func() string {
		t.Helper()
		req := authedRequest(http.MethodPost, "/admin/session", "", adminKey)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		var body struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return body.Token
	}
	first := exchange()
	second := exchange()

	req := authedRequest(http.MethodDelete, "/admin/sessions", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("revoke all: status = %d, want 200: %s", w.Code, w.Body.String())
	}

	for _, token := range []string{first, second} {
		checkReq := tokenRequest(http.MethodGet, "/admin/keys", "", token)
		checkW := httptest.NewRecorder()
		r.ServeHTTP(checkW, checkReq)
		if checkW.Code != http.StatusUnauthorized {
			t.Fatalf("token survived revoke-all: status = %d, want 401", checkW.Code)
		}
	}
}

// exchangeSession posts credential to /admin/session and returns the minted
// token, failing the test on anything but 201.
func exchangeSession(t *testing.T, r chi.Router, credential string) string {
	t.Helper()
	req := tokenRequest(http.MethodPost, "/admin/session", "", credential)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("exchange: status = %d, want 201: %s", w.Code, w.Body.String())
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body.Token
}

// assertSessionStatus hits a scope-gated read with token and requires want.
func assertSessionStatus(t *testing.T, r chi.Router, token string, want int) {
	t.Helper()
	req := tokenRequest(http.MethodGet, "/admin/keys", "", token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != want {
		t.Fatalf("status = %d, want %d: %s", w.Code, want, w.Body.String())
	}
}

// TestSessionRejectedAfterCredentialRevoked is the regression test for the
// defect createSession introduced: a session used to outlive the key it was
// minted from, by up to 24h, because nothing recorded which key that was.
// Runs across every SessionStore backend available in this environment.
func TestSessionRejectedAfterCredentialRevoked(t *testing.T) {
	for name, sessions := range newTestSessionStores(t) {
		t.Run(name, func(t *testing.T) {
			keys := repository.NewKeyStore()
			h, r := routerWithStores(keys, sessions, "")
			adminKey := createAdminKey(t, h)

			token := exchangeSession(t, r, adminKey.Key)
			assertSessionStatus(t, r, token, http.StatusOK) // sanity: live before revoke

			if err := keys.Revoke(t.Context(), adminKey.ID); err != nil {
				t.Fatalf("revoke: %v", err)
			}
			assertSessionStatus(t, r, token, http.StatusUnauthorized)
		})
	}
}

// TestSessionRejectedAfterCredentialDeleted is the same defect, exercised
// through key deletion rather than revocation.
func TestSessionRejectedAfterCredentialDeleted(t *testing.T) {
	for name, sessions := range newTestSessionStores(t) {
		t.Run(name, func(t *testing.T) {
			keys := repository.NewKeyStore()
			h, r := routerWithStores(keys, sessions, "")
			adminKey := createAdminKey(t, h)

			token := exchangeSession(t, r, adminKey.Key)
			assertSessionStatus(t, r, token, http.StatusOK) // sanity: live before delete

			if err := keys.Delete(t.Context(), adminKey.ID); err != nil {
				t.Fatalf("delete: %v", err)
			}
			assertSessionStatus(t, r, token, http.StatusUnauthorized)
		})
	}
}

// TestMasterKeySessionHasNoStoreRowToCheck proves the synthetic-identity
// exemption: the master key authenticates without ever being written to the
// key store, so a session minted from it must not be re-checked against
// Store.Get — that would reject every master-key session outright.
func TestMasterKeySessionHasNoStoreRowToCheck(t *testing.T) {
	const masterKey = "test-master-key"
	for name, sessions := range newTestSessionStores(t) {
		t.Run(name, func(t *testing.T) {
			_, r := routerWithStores(repository.NewKeyStore(), sessions, masterKey)

			token := exchangeSession(t, r, masterKey)
			assertSessionStatus(t, r, token, http.StatusOK)
		})
	}
}

// TestBootstrapKeyCannotMintSession is the session-surface half of the
// ADMIN_BOOTSTRAP_* removal: the exchange handler runs the same credential
// chain as the middleware, so a bootstrap variable must not buy a dashboard
// session either — in the exact condition that path used to admit (no master
// key, no admin key in the store).
func TestBootstrapKeyCannotMintSession(t *testing.T) {
	t.Setenv("ADMIN_BOOTSTRAP_KEY", "bootstrap-secret")
	for name, sessions := range newTestSessionStores(t) {
		t.Run(name, func(t *testing.T) {
			_, r := routerWithStores(repository.NewKeyStore(), sessions, "")

			req := tokenRequest(http.MethodPost, "/admin/session", "", "bootstrap-secret")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401: a bootstrap key still mints a session: %s", w.Code, w.Body.String())
			}
		})
	}
}

// TestMasterKeySessionSurvivesNonEmptyStore pins the master identity's
// liveness gate against over-application: the gate asks only whether the same
// master key is still configured, so a master-key session must keep
// authenticating across a store-population change that has nothing to do with
// it.
func TestMasterKeySessionSurvivesNonEmptyStore(t *testing.T) {
	const masterKey = "test-master-key"
	for name, sessions := range newTestSessionStores(t) {
		t.Run(name, func(t *testing.T) {
			keys := repository.NewKeyStore()
			_, r := routerWithStores(keys, sessions, masterKey)

			token := exchangeSession(t, r, masterKey)
			assertSessionStatus(t, r, token, http.StatusOK) // sanity: live before the store gains a key

			if _, err := keys.Create(t.Context(), "first-real-admin", []string{model.ScopeAdmin}, nil); err != nil {
				t.Fatalf("create: %v", err)
			}
			assertSessionStatus(t, r, token, http.StatusOK)
		})
	}
}

// TestSessionRejectedAfterCredentialExpired is the same defect surface as
// TestSessionRejectedAfterCredentialRevoked/Deleted, exercised through
// expiry rather than revocation or deletion. It exists so a regression that
// narrows the liveness re-check from keyIsUsable(cred) down to cred.Active
// alone — which would still pass the revoke/delete tests, since both of those
// also clear Active — gets caught here instead.
func TestSessionRejectedAfterCredentialExpired(t *testing.T) {
	for name, sessions := range newTestSessionStores(t) {
		t.Run(name, func(t *testing.T) {
			keys := repository.NewKeyStore()
			h, r := routerWithStores(keys, sessions, "")
			adminKey := createAdminKey(t, h)

			token := exchangeSession(t, r, adminKey.Key)
			assertSessionStatus(t, r, token, http.StatusOK) // sanity: live before expiry

			past := time.Now().Add(-time.Minute)
			if err := keys.SetExpiration(t.Context(), adminKey.ID, &past); err != nil {
				t.Fatalf("SetExpiration: %v", err)
			}
			assertSessionStatus(t, r, token, http.StatusUnauthorized)
		})
	}
}

// createKeyViaSession posts a create-key request through a session token and
// returns the response status.
func createKeyViaSession(t *testing.T, r chi.Router, token, name string) int {
	t.Helper()
	req := tokenRequest(http.MethodPost, "/admin/keys", `{"name":"`+name+`"}`, token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code
}

// TestSessionLosesAdminAfterSourceKeyDeScoped is the regression test for
// Fix 1 (blocker): the liveness re-check in AuthMiddlewareWithSessions asked
// only whether the source credential was still usable (keyIsUsable), never
// whether its scopes had changed, so a session kept presenting its mint-time
// scopes for up to 24h after an operator de-scoped the key it came from.
//
// Unlike revoke/delete/expiry, a de-scope must NOT reject the session
// outright — the source key is still usable — it must instead present the
// session downstream with its source credential's current scopes.
func TestSessionLosesAdminAfterSourceKeyDeScoped(t *testing.T) {
	for name, sessions := range newTestSessionStores(t) {
		t.Run(name, func(t *testing.T) {
			keys := repository.NewKeyStore()
			h, r := routerWithStores(keys, sessions, "")
			adminKey := createAdminKey(t, h)

			token := exchangeSession(t, r, adminKey.Key)
			if status := createKeyViaSession(t, r, token, "before-descope"); status != http.StatusCreated {
				t.Fatalf("before de-scope: status = %d, want 201", status)
			}

			if _, err := keys.Update(t.Context(), adminKey.ID, "", []string{model.ScopeReadOnly}); err != nil {
				t.Fatalf("de-scope source key: %v", err)
			}

			// The bug: this used to still return 201 because the identity
			// handed downstream carried the mint-time (admin) scope snapshot.
			if status := createKeyViaSession(t, r, token, "after-descope"); status != http.StatusForbidden {
				t.Fatalf("after de-scope: status = %d, want 403 (session must lose admin immediately)", status)
			}

			// The session itself must still authenticate — a de-scope is a
			// narrowing, not a revocation.
			assertSessionStatus(t, r, token, http.StatusOK)
		})
	}
}

// TestSessionSelfMutationGuard is the regression test for Fix 2 (blocker):
// guardKeyMutation compared APIKeyFromContext(ctx).ID, which for a session is
// the synthetic "session:"-prefixed session ID and can never equal a real key
// ID, so the guard was dead for every session-authenticated request. Every
// case here must be refused exactly as the direct-key equivalents in
// TestSelfMutationGuard are.
func TestSessionSelfMutationGuard(t *testing.T) {
	for name, sessions := range newTestSessionStores(t) {
		t.Run(name, func(t *testing.T) {
			keys := repository.NewKeyStore()
			h, r := routerWithStores(keys, sessions, "")
			adminKey := createAdminKey(t, h)
			// A spare admin so the last-admin guard cannot be what produces
			// the refusal below.
			createTestKey(t, h, "spare-admin", []string{model.ScopeAdmin}, nil)

			token := exchangeSession(t, r, adminKey.Key)

			cases := []struct {
				name   string
				method string
				path   string
				body   string
			}{
				{"revoke", http.MethodPost, "/admin/keys/" + adminKey.ID + "/revoke", ""},
				{"delete", http.MethodDelete, "/admin/keys/" + adminKey.ID, ""},
				{"de-scope", http.MethodPut, "/admin/keys/" + adminKey.ID, `{"scopes":["read_only"]}`},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					req := tokenRequest(tc.method, tc.path, tc.body, token)
					w := httptest.NewRecorder()
					r.ServeHTTP(w, req)
					if w.Code != http.StatusConflict {
						t.Fatalf("status = %d, want 409: %s", w.Code, w.Body.String())
					}
					if got := errorCode(t, w.Body.Bytes()); got != codeSelfMutation {
						t.Fatalf("code = %q, want %q", got, codeSelfMutation)
					}
				})
			}

			// The credential the guard protected must still authenticate:
			// a 409 must not be a partial application of the mutation.
			assertSessionStatus(t, r, token, http.StatusOK)
		})
	}
}

// TestSessionCanMutateADifferentKey proves Fix 2 was not over-applied: a
// session must still be able to mutate a key other than its own source
// credential.
func TestSessionCanMutateADifferentKey(t *testing.T) {
	for name, sessions := range newTestSessionStores(t) {
		t.Run(name, func(t *testing.T) {
			keys := repository.NewKeyStore()
			h, r := routerWithStores(keys, sessions, "")
			adminKey := createAdminKey(t, h)
			target := createTestKey(t, h, "target", []string{model.ScopeAdmin}, nil)

			token := exchangeSession(t, r, adminKey.Key)

			req := tokenRequest(http.MethodPost, "/admin/keys/"+target.ID+"/revoke", "", token)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
			}
		})
	}
}
