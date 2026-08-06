package httpserver_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/internal/admin/model"
	"github.com/ferro-labs/ai-gateway/internal/admin/repository"
	"github.com/ferro-labs/ai-gateway/internal/httpserver"
	"github.com/ferro-labs/ai-gateway/providers"
)

// observabilityScopeFixture builds a router plus one key per scope shape the
// observability routes have to tell apart.
type observabilityScopeFixture struct {
	router   http.Handler
	keyStore repository.Store
	sessions repository.SessionStore

	adminKey    string
	readOnlyKey string
	// unscopedKey carries a scope string nothing recognizes. It stands in for
	// the typo an operator makes creating a key ("adminn"): createKey does not
	// validate scope strings, so such a key authenticates fine and authorizes
	// nothing. Every route that authorizes at all must refuse it.
	unscopedKey string
}

func newObservabilityScopeFixture(t *testing.T) observabilityScopeFixture {
	t.Helper()

	reg := providers.NewRegistry()
	reg.Register(stubProvider{})

	keyStore := repository.NewKeyStore()
	sessions := repository.NewSessionStore()

	mint := func(name string, scopes []string) string {
		t.Helper()
		key, err := keyStore.Create(t.Context(), name, scopes, nil)
		if err != nil {
			t.Fatalf("create %s key: %v", name, err)
		}
		return key.Key
	}

	return observabilityScopeFixture{
		router:      httpserver.NewRouter(reg, keyStore, sessions, nil, nil, nil, nil, nil, nil, nil, "", nil),
		keyStore:    keyStore,
		sessions:    sessions,
		adminKey:    mint("admin", []string{model.ScopeAdmin}),
		readOnlyKey: mint("read-only", []string{model.ScopeReadOnly}),
		unscopedKey: mint("typo", []string{"adminn"}),
	}
}

func (f observabilityScopeFixture) get(t *testing.T, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	return w
}

// session exchanges token for a dashboard session token, the only credential
// the web application ever holds.
func (f observabilityScopeFixture) session(t *testing.T, token string) string {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/admin/session", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /admin/session = %d, want 201: %s", w.Code, w.Body.String())
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode session response: %v", err)
	}
	return body.Token
}

// TestObservabilityRoutes_AuthorizeNotOnlyAuthenticate pins the two tiers the
// observability group is split into. Before this, the group authenticated but
// never authorized: any credential that got past the bearer check read a full
// heap dump, however little its scopes granted it on /admin.
func TestObservabilityRoutes_AuthorizeNotOnlyAuthenticate(t *testing.T) {
	t.Setenv("ENABLE_PPROF", "true")
	f := newObservabilityScopeFixture(t)

	// /metrics is the monitoring tier: a read-only credential reads it, the
	// same tier that reads /admin/dashboard.
	monitoringPaths := []string{"/metrics"}
	// Everything under /debug is the operator tier. /debug/vars is grouped with
	// pprof, not with metrics, because expvar publishes cmdline by default —
	// the process argv, not a counter.
	operatorPaths := []string{"/debug/vars", "/debug/pprof/", "/debug/pprof/cmdline", "/debug/pprof/heap"}

	for _, path := range append(append([]string{}, monitoringPaths...), operatorPaths...) {
		t.Run("admin "+path, func(t *testing.T) {
			if w := f.get(t, path, f.adminKey); w.Code != http.StatusOK {
				t.Fatalf("GET %s with admin key = %d, want 200: %s", path, w.Code, w.Body.String())
			}
		})
		t.Run("unscoped "+path, func(t *testing.T) {
			if w := f.get(t, path, f.unscopedKey); w.Code != http.StatusForbidden {
				t.Fatalf("GET %s with a key whose scope grants nothing = %d, want 403", path, w.Code)
			}
		})
		t.Run("anonymous "+path, func(t *testing.T) {
			if w := f.get(t, path, ""); w.Code != http.StatusUnauthorized {
				t.Fatalf("GET %s unauthenticated = %d, want 401", path, w.Code)
			}
		})
	}

	for _, path := range monitoringPaths {
		t.Run("read_only "+path, func(t *testing.T) {
			if w := f.get(t, path, f.readOnlyKey); w.Code != http.StatusOK {
				t.Fatalf("GET %s with read-only key = %d, want 200: %s", path, w.Code, w.Body.String())
			}
		})
	}

	for _, path := range operatorPaths {
		t.Run("read_only "+path, func(t *testing.T) {
			if w := f.get(t, path, f.readOnlyKey); w.Code != http.StatusForbidden {
				t.Fatalf("GET %s with read-only key = %d, want 403", path, w.Code)
			}
		})
	}
}

// TestObservabilityRoutes_SessionCarriesItsCredentialScopes keeps the dashboard
// working. The web application holds a session token and never a raw key, so a
// session has to reach whatever its source credential could reach directly —
// no more (an admin-only route stays admin-only) and no less (/metrics stays
// readable, which is why the routes accept sessions at all).
func TestObservabilityRoutes_SessionCarriesItsCredentialScopes(t *testing.T) {
	t.Setenv("ENABLE_PPROF", "true")
	f := newObservabilityScopeFixture(t)

	adminSession := f.session(t, f.adminKey)
	readOnlySession := f.session(t, f.readOnlyKey)

	if w := f.get(t, "/metrics", readOnlySession); w.Code != http.StatusOK {
		t.Fatalf("GET /metrics with read-only session = %d, want 200: %s", w.Code, w.Body.String())
	}
	if w := f.get(t, "/metrics", adminSession); w.Code != http.StatusOK {
		t.Fatalf("GET /metrics with admin session = %d, want 200: %s", w.Code, w.Body.String())
	}
	if w := f.get(t, "/debug/pprof/heap", readOnlySession); w.Code != http.StatusForbidden {
		t.Fatalf("GET /debug/pprof/heap with read-only session = %d, want 403", w.Code)
	}
	if w := f.get(t, "/debug/pprof/heap", adminSession); w.Code != http.StatusOK {
		t.Fatalf("GET /debug/pprof/heap with admin session = %d, want 200: %s", w.Code, w.Body.String())
	}
}
