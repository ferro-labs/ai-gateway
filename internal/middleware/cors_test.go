package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

var dummyHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
})

// --- deny-by-default (no origins configured) ---

func TestCORS_NoCORSHeaders_WhenNoOriginsConfigured(t *testing.T) {
	mw := CORS()
	handler := mw(dummyHandler)

	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/chat/completions", nil)
	r.Header.Set("Origin", "https://attacker.example.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("expected no Access-Control-Allow-Origin header when no origins configured, got %q", got)
	}
}

func TestCORS_NoCORSHeaders_WhenOnlyEmptyStrings(t *testing.T) {
	mw := CORS("", "  ")
	handler := mw(dummyHandler)

	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	r.Header.Set("Origin", "https://attacker.example.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("expected no Access-Control-Allow-Origin header when only empty strings configured, got %q", got)
	}
}

// TestCORS_NoCORSHeaders_OnAdminPath verifies that the deny-by-default behaviour
// applies to sensitive /admin/* paths when no origins are configured.
func TestCORS_NoCORSHeaders_OnAdminPath(t *testing.T) {
	mw := CORS()
	handler := mw(dummyHandler)

	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/admin/keys", nil)
	r.Header.Set("Origin", "https://attacker.example.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("expected no Access-Control-Allow-Origin on /admin path when no origins configured, got %q", got)
	}
	// None of the other CORS response headers should be present either.
	if got := w.Header().Get("Access-Control-Allow-Headers"); got != "" {
		t.Fatalf("expected no Access-Control-Allow-Headers on /admin path, got %q", got)
	}
}

// TestCORS_Preflight_AnsweredHere_DeniedByHeader pins where a cross-origin
// request is denied and how.
//
// A preflight is answered by this middleware whether or not the origin is
// allowed, and a denial is the absence of Access-Control-Allow-Origin — the
// only part of the response a browser enforces. Letting a denied preflight fall
// through instead handed it to layers that answer a different question: it
// carries no credentials, so authentication called it 401, and the route's
// method guard serves POST, so it called it 405 "Allow: POST" — on a route that
// answers OPTIONS with 204 as soon as the origin is listed.
func TestCORS_Preflight_AnsweredHere_DeniedByHeader(t *testing.T) {
	tests := []struct {
		name        string
		origins     []string
		origin      string
		wantStatus  int
		wantAllowed string // expected Access-Control-Allow-Origin ("" = absent)
		wantNext    bool
	}{
		{
			name:       "no allowlist configured",
			origins:    nil,
			origin:     "https://app.example.com",
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "allowlist configured, origin not on it",
			origins:    []string{"https://allowed.example.com"},
			origin:     "https://attacker.example.com",
			wantStatus: http.StatusNoContent,
		},
		{
			name:        "allowlist configured, origin on it",
			origins:     []string{"https://allowed.example.com"},
			origin:      "https://allowed.example.com",
			wantStatus:  http.StatusNoContent,
			wantAllowed: "https://allowed.example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				called = true
				w.WriteHeader(http.StatusOK)
			})
			handler := CORS(tt.origins...)(next)

			r := httptest.NewRequestWithContext(t.Context(), http.MethodOptions, "/v1/chat/completions", nil)
			r.Header.Set("Origin", tt.origin)
			r.Header.Set("Access-Control-Request-Method", http.MethodPost)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)

			if w.Code != tt.wantStatus {
				t.Errorf("status %d, want %d — a preflight must not be refused by status", w.Code, tt.wantStatus)
			}
			if got := w.Header().Get("Access-Control-Allow-Origin"); got != tt.wantAllowed {
				t.Errorf("Access-Control-Allow-Origin %q, want %q — this header is the whole denial", got, tt.wantAllowed)
			}
			if called != tt.wantNext {
				t.Errorf("next handler called = %v, want %v", called, tt.wantNext)
			}
			if got := w.Header().Get("Vary"); got != "Origin" {
				t.Errorf("Vary %q, want Origin — the response depends on Origin either way", got)
			}
		})
	}
}

// TestCORS_OptionsWithoutOrigin_PassesThrough guards the narrow edge of the rule
// above. Only OPTIONS carrying an Origin is a preflight; a bare OPTIONS is a
// genuine question about the resource's methods, so it must still reach the
// route and get that route's own answer.
func TestCORS_OptionsWithoutOrigin_PassesThrough(t *testing.T) {
	for _, origins := range [][]string{nil, {"https://allowed.example.com"}} {
		called := false
		next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			called = true
			w.WriteHeader(http.StatusOK)
		})
		handler := CORS(origins...)(next)

		r := httptest.NewRequestWithContext(t.Context(), http.MethodOptions, "/v1/chat/completions", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)

		if !called {
			t.Errorf("origins=%v: OPTIONS without an Origin is not a preflight and must reach the route", origins)
		}
	}
}

// --- allowlist configured ---

func TestCORS_AllowedOrigin_SetsHeaderAndVary(t *testing.T) {
	mw := CORS("https://example.com", "https://other.com")
	handler := mw(dummyHandler)

	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	r.Header.Set("Origin", "https://example.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://example.com" {
		t.Fatalf("expected https://example.com, got %q", got)
	}
	if got := w.Header().Get("Vary"); got != "Origin" {
		t.Fatalf("expected Vary: Origin, got %q", got)
	}
}

func TestCORS_DisallowedOrigin_NoAllowOriginHeader(t *testing.T) {
	mw := CORS("https://example.com")
	handler := mw(dummyHandler)

	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	r.Header.Set("Origin", "https://evil.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("expected no Allow-Origin header, got %q", got)
	}
}

func TestCORS_PreflightOptions_Returns204(t *testing.T) {
	mw := CORS("https://example.com")
	handler := mw(dummyHandler)

	r := httptest.NewRequestWithContext(t.Context(), http.MethodOptions, "/", nil)
	r.Header.Set("Origin", "https://example.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", w.Code)
	}
}

func TestCORS_PreflightOptions_DoesNotCallNext(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		called = true
	})
	mw := CORS("https://example.com")
	handler := mw(next)

	r := httptest.NewRequestWithContext(t.Context(), http.MethodOptions, "/", nil)
	r.Header.Set("Origin", "https://example.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if called {
		t.Fatal("next handler should not be called for OPTIONS preflight")
	}
}

// TestCORS_WithConfiguredOrigin_PreflightGetsAllowHeaders verifies that a
// preflight OPTIONS request from a configured origin receives both
// Access-Control-Allow-Origin and Access-Control-Allow-Headers.
func TestCORS_WithConfiguredOrigin_PreflightGetsAllowHeaders(t *testing.T) {
	mw := CORS("https://dash.example.com")
	handler := mw(dummyHandler)

	r := httptest.NewRequestWithContext(t.Context(), http.MethodOptions, "/v1/chat/completions", nil)
	r.Header.Set("Origin", "https://dash.example.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://dash.example.com" {
		t.Fatalf("expected Access-Control-Allow-Origin: https://dash.example.com, got %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Headers"); got == "" {
		t.Fatal("expected non-empty Access-Control-Allow-Headers for preflight from configured origin")
	}
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", w.Code)
	}
}

func TestCORS_StandardHeaders_AlwaysSet(t *testing.T) {
	mw := CORS("https://example.com")
	handler := mw(dummyHandler)

	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	r.Header.Set("Origin", "https://example.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if got := w.Header().Get("Access-Control-Allow-Methods"); got != "GET, POST, PUT, DELETE, OPTIONS" {
		t.Fatalf("unexpected Allow-Methods: %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Headers"); got != "Content-Type, Authorization, X-Provider, X-User-ID, X-Session-ID, Baggage" {
		t.Fatalf("unexpected Allow-Headers: %q", got)
	}
	if got := w.Header().Get("Access-Control-Max-Age"); got != "86400" {
		t.Fatalf("unexpected Max-Age: %q", got)
	}
}

func TestCORS_IdentityHeadersPreflight(t *testing.T) {
	for _, origin := range []string{"https://app.example", "https://untrusted.example"} {
		t.Run(origin, func(t *testing.T) {
			handler := CORS("https://app.example")(dummyHandler)
			req := httptest.NewRequestWithContext(t.Context(), http.MethodOptions, "/v1/chat/completions", nil)
			req.Header.Set("Origin", origin)
			req.Header.Set("Access-Control-Request-Method", "POST")
			req.Header.Set("Access-Control-Request-Headers", "x-user-id,x-session-id,baggage")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want 204", rec.Code)
			}
			if origin != "https://app.example" {
				if rec.Header().Get("Access-Control-Allow-Origin") != "" || rec.Header().Get("Access-Control-Allow-Headers") != "" {
					t.Fatal("untrusted origin received CORS permission")
				}
				return
			}
			allowed := make(map[string]bool)
			for _, header := range strings.Split(rec.Header().Get("Access-Control-Allow-Headers"), ",") {
				allowed[strings.ToLower(strings.TrimSpace(header))] = true
			}
			for _, header := range []string{"x-user-id", "x-session-id", "baggage"} {
				if !allowed[header] {
					t.Errorf("preflight does not allow %s", header)
				}
			}
		})
	}
}

func TestCORS_NonOptions_CallsNextHandler(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	mw := CORS()
	handler := mw(next)

	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if !called {
		t.Fatal("next handler should be called for non-OPTIONS request")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestCORS_TrimsWhitespaceFromOrigins(t *testing.T) {
	mw := CORS("  https://example.com  ")
	handler := mw(dummyHandler)

	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	r.Header.Set("Origin", "https://example.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://example.com" {
		t.Fatalf("expected https://example.com, got %q", got)
	}
}
