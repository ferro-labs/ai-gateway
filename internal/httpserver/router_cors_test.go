package httpserver_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/internal/admin/repository"
	"github.com/ferro-labs/ai-gateway/internal/httpserver"
	"github.com/ferro-labs/ai-gateway/providers"
)

// buildCORSTestRouter builds a router with the given CORS allowlist. Auth stays
// on: a browser preflight carries no credentials, so the point of the test is
// that it is answered without ever reaching the credential check.
func buildCORSTestRouter(t *testing.T, origins []string) http.Handler {
	t.Helper()
	reg := providers.NewRegistry()
	reg.Register(stubProvider{})
	return httpserver.NewRouter(reg, repository.NewKeyStore(), repository.NewSessionStore(),
		origins, nil, nil, nil, nil, nil, nil, "", nil)
}

// TestPreflight_ReachesCORS_NotAuthOrMethodGuard pins the layer that answers a
// CORS preflight.
//
// A preflight was refused before CORS could answer it: it carries no
// Authorization header, so the auth middleware answered 401, and where auth was
// disabled the route's method guard answered 405 with "Allow: POST". Neither is
// a CORS answer, and neither is true of the resource — the same route answers
// OPTIONS with 204 the moment the caller's origin is listed, so method support
// is not what is being decided.
//
// The denial that matters is the absent Access-Control-Allow-Origin, which is
// the part a browser enforces. The status must stay a success so the failure the
// developer sees names CORS rather than a method or a credential.
func TestPreflight_ReachesCORS_NotAuthOrMethodGuard(t *testing.T) {
	const allowedOrigin = "https://allowed.example.com"

	tests := []struct {
		name        string
		origins     []string
		origin      string
		wantAllowed string // expected Access-Control-Allow-Origin ("" = absent)
	}{
		{
			name:    "no allowlist configured",
			origins: nil,
			origin:  "https://app.example.com",
		},
		{
			name:    "allowlist configured, origin not on it",
			origins: []string{allowedOrigin},
			origin:  "https://attacker.example.com",
		},
		{
			name:        "allowlist configured, origin on it",
			origins:     []string{allowedOrigin},
			origin:      allowedOrigin,
			wantAllowed: allowedOrigin,
		},
	}

	// Every routed surface, so the answer cannot differ per endpoint.
	paths := []string{
		"/v1/chat/completions",
		"/v1/completions",
		"/v1/embeddings",
		"/v1/images/generations",
		"/v1/rerank",
		"/v1/moderations",
		"/v1/audio/speech",
		"/v1/audio/transcriptions",
		"/v1/audio/translations",
		"/v1/files",
		"/v1/batches",
		"/v1/responses",
		"/v1/models",
		"/admin/keys",
		// The probes are mounted on the root router to stay clear of the rate
		// limiter, so they reach CORS by a different path than everything above
		// and were the one surface this list did not cover. Giving them a
		// per-method guard answered the preflight from the route instead — a 405
		// whose Allow is true of the resource and irrelevant to what was asked.
		"/health",
		"/livez",
		"/readyz",
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := buildCORSTestRouter(t, tt.origins)

			for _, path := range paths {
				t.Run(path, func(t *testing.T) {
					req := httptest.NewRequestWithContext(t.Context(), http.MethodOptions, path, nil)
					req.Header.Set("Origin", tt.origin)
					req.Header.Set("Access-Control-Request-Method", http.MethodPost)
					req.Header.Set("Access-Control-Request-Headers", "content-type")
					w := httptest.NewRecorder()
					router.ServeHTTP(w, req)

					if w.Code != http.StatusNoContent {
						t.Errorf("OPTIONS %s = %d, want 204 — the preflight was answered by auth or the method guard, not by CORS (body: %s)",
							path, w.Code, w.Body.String())
					}
					if got := w.Header().Get("Access-Control-Allow-Origin"); got != tt.wantAllowed {
						t.Errorf("OPTIONS %s: Access-Control-Allow-Origin = %q, want %q", path, got, tt.wantAllowed)
					}
					// A refusal status would be described by Allow; a preflight
					// answer must not be, because method support is not what
					// was decided.
					if got := w.Header().Get("Allow"); got != "" {
						t.Errorf("OPTIONS %s: preflight answer carried Allow: %q", path, got)
					}
				})
			}
		})
	}
}

// TestOptionsWithoutOrigin_StillRefusedByRoute guards the edge of the rule
// above. Only OPTIONS carrying an Origin is a preflight. A bare OPTIONS is a
// real question about the resource's methods, so the route must still answer it
// — with its own 405 and an accurate Allow.
func TestOptionsWithoutOrigin_StillRefusedByRoute(t *testing.T) {
	t.Setenv("ALLOW_UNAUTHENTICATED_PROXY", "true")
	router := buildCORSTestRouter(t, []string{"https://allowed.example.com"})

	req := httptest.NewRequestWithContext(t.Context(), http.MethodOptions, "/v1/chat/completions", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("OPTIONS without Origin = %d, want 405 — it is not a preflight and belongs to the route (body: %s)",
			w.Code, w.Body.String())
	}
	if got := w.Header().Get("Allow"); got != "POST" {
		t.Errorf("Allow = %q, want POST", got)
	}
}
