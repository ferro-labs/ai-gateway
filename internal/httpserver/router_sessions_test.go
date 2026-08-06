package httpserver_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/internal/admin/model"
	"github.com/ferro-labs/ai-gateway/internal/admin/repository"
	"github.com/ferro-labs/ai-gateway/internal/httpserver"
	"github.com/ferro-labs/ai-gateway/providers"
)

// TestRouter_SessionAuthenticatesThroughProductionWiring is the one place the
// same-store invariant AuthMiddlewareWithSessions depends on is actually
// exercised end to end. Every other router test in this package passes nil
// for the session store, and every smoke test authenticates with the master
// key -- whose credential ID is synthetic and takes the
// syntheticCredentialLive exemption branch in AuthMiddlewareWithSessions,
// never reaching store.Get(sess.CredentialID). Substituting a different
// repository.Store instance at that call site would leave the rest of this
// package green. This test uses a stored admin key -- not the master key --
// so it is the one path that actually calls store.Get for the session's
// source credential, through the exact production wiring in
// httpserver.NewRouter (mountAdminRoutes and mountOpenAIRoutes).
func TestRouter_SessionAuthenticatesThroughProductionWiring(t *testing.T) {
	reg := providers.NewRegistry()
	reg.Register(stubProvider{})

	keyStore := repository.NewKeyStore()
	sessionStore := repository.NewSessionStore()

	created, err := keyStore.Create(t.Context(), "dashboard-admin", []string{model.ScopeAdmin}, nil)
	if err != nil {
		t.Fatalf("create admin key: %v", err)
	}

	router := httpserver.NewRouter(reg, keyStore, sessionStore, nil, nil, nil, nil, nil, nil, nil, "", nil)

	do := func(req *http.Request) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	bearerReq := func(method, path, token string) *http.Request {
		req := httptest.NewRequestWithContext(t.Context(), method, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		return req
	}

	// 1. Exchange the stored admin key for a session.
	sessW := do(bearerReq(http.MethodPost, "/admin/session", created.Key))
	if sessW.Code != http.StatusCreated {
		t.Fatalf("POST /admin/session = %d, want 201: %s", sessW.Code, sessW.Body.String())
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(sessW.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode session response: %v", err)
	}
	if !strings.HasPrefix(body.Token, model.SessionTokenPrefix) {
		t.Fatalf("token %q lacks the session prefix", body.Token)
	}

	// 2. The session authenticates a normal admin read.
	if w := do(bearerReq(http.MethodGet, "/admin/keys", body.Token)); w.Code != http.StatusOK {
		t.Fatalf("GET /admin/keys with session = %d, want 200: %s", w.Code, w.Body.String())
	}

	// 3. The session also authenticates on the proxy path (dashboard Playground).
	if w := do(bearerReq(http.MethodGet, "/v1/models", body.Token)); w.Code != http.StatusOK {
		t.Fatalf("GET /v1/models with session = %d, want 200: %s", w.Code, w.Body.String())
	}

	// 4. Revoke the underlying key.
	if err := keyStore.Revoke(t.Context(), created.ID); err != nil {
		t.Fatalf("revoke key: %v", err)
	}

	// 5. The session must no longer authenticate: this is the liveness
	// re-check (store.Get(sess.CredentialID) + keyIsUsable) running through
	// the actual production wiring, not a direct unit test of the middleware.
	if w := do(bearerReq(http.MethodGet, "/admin/keys", body.Token)); w.Code != http.StatusUnauthorized {
		t.Fatalf("GET /admin/keys after revoke = %d, want 401: %s", w.Code, w.Body.String())
	}
}
