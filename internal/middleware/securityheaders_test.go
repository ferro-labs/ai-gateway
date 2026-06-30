package middleware

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSecurityHeaders_SetsBaselineHeaders(t *testing.T) {
	handler := SecurityHeaders(dummyHandler)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	tests := []struct {
		header string
		want   string
	}{
		{"X-Content-Type-Options", "nosniff"},
		{"X-Frame-Options", "DENY"},
		{"Referrer-Policy", "strict-origin-when-cross-origin"},
	}
	for _, tt := range tests {
		if got := w.Header().Get(tt.header); got != tt.want {
			t.Errorf("header %s = %q, want %q", tt.header, got, tt.want)
		}
	}
}

func TestSecurityHeaders_NoHSTS_WhenNotTLS(t *testing.T) {
	handler := SecurityHeaders(dummyHandler)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	// r.TLS is nil by default — plain HTTP
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if got := w.Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("expected no Strict-Transport-Security on plain HTTP, got %q", got)
	}
}

func TestSecurityHeaders_HSTS_WhenTLS(t *testing.T) {
	handler := SecurityHeaders(dummyHandler)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
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

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if !called {
		t.Fatal("next handler was not called")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}
