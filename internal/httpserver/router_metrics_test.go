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

// TestRouter_ObservabilityAcceptsDashboardSession covers the credential the
// dashboard actually holds. The observability routes were gated on the API-key
// chain alone, so a session bearer fell through it and got 401 — leaving the web
// application unable to read the metrics it exists to display, with no raw key
// to fall back on.
func TestRouter_ObservabilityAcceptsDashboardSession(t *testing.T) {
	reg := providers.NewRegistry()
	reg.Register(stubProvider{})

	keyStore := repository.NewKeyStore()
	sessionStore := repository.NewSessionStore()

	created, err := keyStore.Create(t.Context(), "dashboard-admin", []string{model.ScopeAdmin}, nil)
	if err != nil {
		t.Fatalf("create admin key: %v", err)
	}

	router := httpserver.NewRouter(reg, keyStore, sessionStore, nil, nil, nil, nil, nil, nil, nil, "", nil)

	do := func(path, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	// Exchange the stored admin key for a dashboard session.
	exchangeReq := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/admin/session", nil)
	exchangeReq.Header.Set("Authorization", "Bearer "+created.Key)
	exchangeW := httptest.NewRecorder()
	router.ServeHTTP(exchangeW, exchangeReq)
	if exchangeW.Code != http.StatusCreated {
		t.Fatalf("POST /admin/session = %d, want 201: %s", exchangeW.Code, exchangeW.Body.String())
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(exchangeW.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode session response: %v", err)
	}

	for _, path := range []string{"/metrics", "/debug/vars"} {
		t.Run(path, func(t *testing.T) {
			if w := do(path, body.Token); w.Code != http.StatusOK {
				t.Fatalf("GET %s with session = %d, want 200: %s", path, w.Code, w.Body.String())
			}
			// The raw key must keep working: sessions are an addition, not a
			// replacement, and scrapers present a key.
			if w := do(path, created.Key); w.Code != http.StatusOK {
				t.Fatalf("GET %s with API key = %d, want 200: %s", path, w.Code, w.Body.String())
			}
			// Still gated: an unauthenticated scrape is refused as before.
			if w := do(path, ""); w.Code != http.StatusUnauthorized {
				t.Fatalf("GET %s unauthenticated = %d, want 401", path, w.Code)
			}
			if w := do(path, "not-a-real-credential"); w.Code != http.StatusUnauthorized {
				t.Fatalf("GET %s with a bad bearer = %d, want 401", path, w.Code)
			}
		})
	}

	// Revoking the source key must cut the session off here too, the same
	// liveness re-check /admin/* performs.
	if err := keyStore.Revoke(t.Context(), created.ID); err != nil {
		t.Fatalf("revoke key: %v", err)
	}
	if w := do("/metrics", body.Token); w.Code != http.StatusUnauthorized {
		t.Fatalf("GET /metrics after revoke = %d, want 401", w.Code)
	}
}

// TestObservabilityRoutes_MethodNarrowing pins /metrics and /debug/vars to the
// methods they actually serve.
//
// Both were registered for every method, so PUT /metrics returned the whole
// metric dump and POST /debug/vars returned expvar including the process command
// line. They were also the only natively-mounted paths that could never produce
// a 405, which is the only reason "every 405 carries an accurate Allow" held.
func TestObservabilityRoutes_MethodNarrowing(t *testing.T) {
	reg := providers.NewRegistry()
	reg.Register(stubProvider{})
	keyStore := repository.NewKeyStore()

	created, err := keyStore.Create(t.Context(), "admin", []string{model.ScopeAdmin}, nil)
	if err != nil {
		t.Fatalf("create admin key: %v", err)
	}
	router := httpserver.NewRouter(reg, keyStore, repository.NewSessionStore(), nil, nil, nil, nil, nil, nil, nil, "", nil)

	call := func(method, path, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), method, path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	const wantAllow = "GET, HEAD"

	for _, path := range []string{"/metrics", "/debug/vars"} {
		t.Run(path, func(t *testing.T) {
			tests := []struct {
				method string
				want   int
			}{
				{http.MethodGet, http.StatusOK},
				{http.MethodHead, http.StatusOK},
				{http.MethodPut, http.StatusMethodNotAllowed},
				{http.MethodPost, http.StatusMethodNotAllowed},
				{http.MethodDelete, http.StatusMethodNotAllowed},
				{http.MethodPatch, http.StatusMethodNotAllowed},
				{http.MethodOptions, http.StatusMethodNotAllowed},
			}

			for _, tt := range tests {
				t.Run(tt.method, func(t *testing.T) {
					w := call(tt.method, path, created.Key)
					if w.Code != tt.want {
						t.Fatalf("%s %s = %d, want %d (body: %s)", tt.method, path, w.Code, tt.want, w.Body.String())
					}
					if tt.want != http.StatusMethodNotAllowed {
						return
					}
					// A refusal keeps the contract the other native routes
					// established: an accurate Allow and the JSON envelope.
					if got := w.Header().Get("Allow"); got != wantAllow {
						t.Errorf("%s %s: Allow = %q, want %q", tt.method, path, got, wantAllow)
					}
					if got := w.Header().Get("Content-Type"); got != "application/json" {
						t.Errorf("%s %s: Content-Type = %q, want application/json", tt.method, path, got)
					}
					var envelope struct {
						Error struct {
							Message string `json:"message"`
							Type    string `json:"type"`
							Code    string `json:"code"`
						} `json:"error"`
					}
					if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
						t.Fatalf("%s %s: refusal body is not the JSON error envelope: %v (body: %s)",
							tt.method, path, err, w.Body.String())
					}
					if envelope.Error.Code != "method_not_supported" {
						t.Errorf("%s %s: error.code = %q, want method_not_supported", tt.method, path, envelope.Error.Code)
					}
				})
			}

			// The body must not leak through the refusal. /metrics would carry
			// the metric dump and /debug/vars the process command line.
			if body := call(http.MethodPut, path, created.Key).Body.String(); strings.Contains(body, "cmdline") ||
				strings.Contains(body, "go_goroutines") {
				t.Errorf("PUT %s: refusal body carried the reporter's payload: %s", path, body)
			}

			// Narrowing the method must not weaken the scope gate: an
			// unauthenticated wrong method is still 401 and never learns which
			// methods the path serves.
			w := call(http.MethodPut, path, "")
			if w.Code != http.StatusUnauthorized {
				t.Errorf("PUT %s unauthenticated = %d, want 401", path, w.Code)
			}
			if got := w.Header().Get("Allow"); got != "" {
				t.Errorf("PUT %s unauthenticated: leaked Allow: %q", path, got)
			}
		})
	}
}
