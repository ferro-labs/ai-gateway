package mcp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestClientRedirectErrorNamesTarget pins REG-004 for MCP.
//
// The client POSTs its configured endpoint verbatim, so the likeliest 3xx it
// meets is a server answering /mcp with a same-host 307 to /mcp/ — the exact
// shape the repo's own example configs would hit. Before this, that and a
// cross-host hop produced the same message ("returned HTTP 307: "), which named
// neither the target nor which of the two had happened, while a required:true
// server took the whole instance out of rotation.
func TestClientRedirectErrorNamesTarget(t *testing.T) {
	t.Run("same-host trailing slash", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, r.URL.Path+"/", http.StatusTemporaryRedirect)
		}))
		defer srv.Close()

		_, err := NewClient(srv.URL+"/mcp", nil, 5*time.Second).Initialize(t.Context())
		if err == nil {
			t.Fatal("Initialize succeeded against a redirecting server")
		}
		host := strings.TrimPrefix(srv.URL, "http://")
		if !strings.Contains(err.Error(), host) {
			t.Errorf("error %q does not name the redirect target host %q", err, host)
		}
		if !strings.Contains(err.Error(), "same host") {
			t.Errorf("error %q does not say the redirect was same-host, which is what tells a URL typo apart from a credential replay", err)
		}
	})

	t.Run("cross-host", func(t *testing.T) {
		elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer elsewhere.Close()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, elsewhere.URL+"/mcp", http.StatusFound)
		}))
		defer srv.Close()

		_, err := NewClient(srv.URL+"/mcp", nil, 5*time.Second).Initialize(t.Context())
		if err == nil {
			t.Fatal("Initialize succeeded against a redirecting server")
		}
		target := strings.TrimPrefix(elsewhere.URL, "http://")
		if !strings.Contains(err.Error(), target) {
			t.Errorf("error %q does not name the cross-host redirect target %q", err, target)
		}
		if strings.Contains(err.Error(), "same host") {
			t.Errorf("error %q calls a cross-host redirect same-host", err)
		}
	})
}
