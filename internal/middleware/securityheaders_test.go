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

// The policy protects an admin token held in localStorage and, now that the
// gateway serves the dashboard itself, the page holding it. Asserting each
// directive's full value rather than a substring means a future edit that
// widens one — adding a host to script-src, a scheme to connect-src — fails
// here instead of shipping.
func TestSecurityHeaders_ContentSecurityPolicyDirectives(t *testing.T) {
	served := servedHeaders(t, false).Get("Content-Security-Policy")

	got := map[string]string{}
	for _, directive := range strings.Split(served, ";") {
		name, value, _ := strings.Cut(strings.TrimSpace(directive), " ")
		got[name] = value
	}

	tests := []struct {
		name  string
		value string
	}{
		{"default-src", "'self'"},
		{"base-uri", "'self'"},
		{"form-action", "'self'"},
		{"frame-ancestors", "'none'"},
		{"object-src", "'none'"},
		{"script-src", "'self'"},
		{"style-src", "'self' 'sha256-kLmvWqfziFavKtqHqRsb90f006UAK2Dmd0It5Iz2KFA='"},
		{"font-src", "'self'"},
		{"img-src", "'self' data:"},
		// The dashboard is served by the gateway it calls, so no cross-origin
		// endpoint has to be allowed here.
		{"connect-src", "'self'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, ok := got[tt.name]
			if !ok {
				t.Fatalf("CSP has no %s directive: %s", tt.name, served)
			}
			if value != tt.value {
				t.Errorf("%s = %q, want %q", tt.name, value, tt.value)
			}
		})
	}
	if len(got) != len(tests) {
		t.Errorf("CSP has %d directives, want %d: %s", len(got), len(tests), served)
	}
}

// The single stylesheet allowed by digest is the only inline style the policy
// permits. 'unsafe-inline' anywhere would not add to that — in style-src it
// disables the digest and admits any inline style, and in script-src it defeats
// the policy outright.
func TestSecurityHeaders_ContentSecurityPolicyForbidsUnsafeSources(t *testing.T) {
	served := servedHeaders(t, false).Get("Content-Security-Policy")

	for _, unsafe := range []string{"unsafe-inline", "unsafe-eval", "unsafe-hashes"} {
		if strings.Contains(served, unsafe) {
			t.Errorf("CSP must not contain %q: %s", unsafe, served)
		}
	}
	if !strings.Contains(served, "'sha256-kLmvWqfziFavKtqHqRsb90f006UAK2Dmd0It5Iz2KFA='") {
		t.Errorf("CSP is missing the stylesheet digest the dashboard needs: %s", served)
	}
}

// HSTS is emitted only over TLS: a browser ignores it on plain HTTP anyway, and
// sending it unconditionally would pin a plain-HTTP local deployment to a scheme
// it does not serve.
func TestSecurityHeaders_StrictTransportSecurity(t *testing.T) {
	tests := []struct {
		name string
		tls  bool
		want string
	}{
		{"plain HTTP", false, ""},
		{"TLS", true, "max-age=31536000; includeSubDomains"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := servedHeaders(t, tt.tls).Get("Strict-Transport-Security")
			if got != tt.want {
				t.Errorf("Strict-Transport-Security = %q, want %q", got, tt.want)
			}
		})
	}
}

// servedHeaders runs one request through the middleware and returns what a
// browser would receive, so tests assert the response rather than the constant.
func servedHeaders(t *testing.T, useTLS bool) http.Header {
	t.Helper()

	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	if useTLS {
		r.TLS = &tls.ConnectionState{}
	}
	w := httptest.NewRecorder()
	SecurityHeaders(dummyHandler).ServeHTTP(w, r)
	return w.Header()
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
