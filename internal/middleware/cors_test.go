package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

var dummyHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
})

// --- deny-by-default (no origins configured) ---

func TestCORS_NoCORSHeaders_WhenNoOriginsConfigured(t *testing.T) {
	mw := CORS()
	handler := mw(dummyHandler)

	r := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
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

	r := httptest.NewRequest(http.MethodGet, "/", nil)
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

	r := httptest.NewRequest(http.MethodGet, "/admin/keys", nil)
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

// TestCORS_NoOriginsConfigured_OptionsPassesThrough verifies that an OPTIONS
// preflight is passed through to the next handler (not short-circuited with 204)
// when no origins are configured.
func TestCORS_NoOriginsConfigured_OptionsPassesThrough(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	mw := CORS()
	handler := mw(next)

	r := httptest.NewRequest(http.MethodOptions, "/v1/chat/completions", nil)
	r.Header.Set("Origin", "https://attacker.example.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if !called {
		t.Fatal("next handler should be called for OPTIONS when no origins configured (pass-through)")
	}
}

// --- allowlist configured ---

func TestCORS_AllowedOrigin_SetsHeaderAndVary(t *testing.T) {
	mw := CORS("https://example.com", "https://other.com")
	handler := mw(dummyHandler)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
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

	r := httptest.NewRequest(http.MethodGet, "/", nil)
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

	r := httptest.NewRequest(http.MethodOptions, "/", nil)
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

	r := httptest.NewRequest(http.MethodOptions, "/", nil)
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

	r := httptest.NewRequest(http.MethodOptions, "/v1/chat/completions", nil)
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

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Origin", "https://example.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if got := w.Header().Get("Access-Control-Allow-Methods"); got != "GET, POST, PUT, DELETE, OPTIONS" {
		t.Fatalf("unexpected Allow-Methods: %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Headers"); got != "Content-Type, Authorization, X-Provider" {
		t.Fatalf("unexpected Allow-Headers: %q", got)
	}
	if got := w.Header().Get("Access-Control-Max-Age"); got != "86400" {
		t.Fatalf("unexpected Max-Age: %q", got)
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

	r := httptest.NewRequest(http.MethodGet, "/", nil)
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

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Origin", "https://example.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://example.com" {
		t.Fatalf("expected https://example.com, got %q", got)
	}
}
