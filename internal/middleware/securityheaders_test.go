package middleware

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurityHeaders_SetsBaselineHeaders(t *testing.T) {
	handler := SecurityHeaders(dummyHandler)

	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	tests := []struct {
		header string
		want   string
	}{
		{"X-Content-Type-Options", "nosniff"},
		{"X-Frame-Options", "DENY"},
		{"Referrer-Policy", "strict-origin-when-cross-origin"},
		{"Content-Security-Policy", ContentSecurityPolicy},
		{"Permissions-Policy", PermissionsPolicy},
	}
	for _, tt := range tests {
		if got := w.Header().Get(tt.header); got != tt.want {
			t.Errorf("header %s = %q, want %q", tt.header, got, tt.want)
		}
	}
}

// The policy protects an admin token held in localStorage. These are the
// directives that do that work; a future edit must not quietly relax them.
func TestSecurityHeaders_ContentSecurityPolicyBlocksInjectedScript(t *testing.T) {
	mustContain := []string{
		"script-src 'self'",
		"object-src 'none'",
		"frame-ancestors 'none'",
		"base-uri 'self'",
	}
	for _, directive := range mustContain {
		if !strings.Contains(ContentSecurityPolicy, directive) {
			t.Errorf("CSP is missing %q: %s", directive, ContentSecurityPolicy)
		}
	}

	// 'unsafe-inline'/'unsafe-eval' in script-src would defeat the whole policy.
	// It is allowed in style-src, so check the script-src directive alone.
	scriptSrc := ""
	for _, directive := range strings.Split(ContentSecurityPolicy, ";") {
		if strings.HasPrefix(strings.TrimSpace(directive), "script-src") {
			scriptSrc = directive
		}
	}
	if strings.Contains(scriptSrc, "unsafe-inline") || strings.Contains(scriptSrc, "unsafe-eval") {
		t.Fatalf("script-src must not allow unsafe script execution: %q", scriptSrc)
	}
}

func TestSecurityHeaders_NoHSTS_WhenNotTLS(t *testing.T) {
	handler := SecurityHeaders(dummyHandler)

	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	// r.TLS is nil by default — plain HTTP
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if got := w.Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("expected no Strict-Transport-Security on plain HTTP, got %q", got)
	}
}

func TestSecurityHeaders_HSTS_WhenTLS(t *testing.T) {
	handler := SecurityHeaders(dummyHandler)

	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	r.TLS = &tls.ConnectionState{}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	want := "max-age=31536000; includeSubDomains"
	if got := w.Header().Get("Strict-Transport-Security"); got != want {
		t.Errorf("Strict-Transport-Security = %q, want %q", got, want)
	}
}

func TestSecurityHeaders_CallsNextHandler(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	handler := SecurityHeaders(next)

	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if !called {
		t.Fatal("next handler was not called")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}
