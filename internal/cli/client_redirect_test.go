package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestAdminClientDoesNotFollowRedirects pins the CLI half of SEC-002's harm
// frame. Every admin call carries the operator's key as a bearer token, so a
// followed redirect would replay that credential to whatever host the response
// names.
func TestAdminClientDoesNotFollowRedirects(t *testing.T) {
	const sentinel = "sentinel-not-a-real-credential-0002"

	var (
		reached bool
		gotAuth string
	)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusFound)
	}))
	defer redirector.Close()

	c := &AdminClient{
		BaseURL: redirector.URL,
		APIKey:  sentinel,
		HTTPClient: &http.Client{
			Timeout: 5 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}

	var out map[string]any
	if err := c.Get(t.Context(), "/admin/keys", &out); err == nil {
		t.Error("Get followed the redirect and reported success; want the 302 surfaced as an error")
	}
	if reached {
		t.Error("the redirect target was contacted; the admin client followed the 302")
	}
	if gotAuth != "" {
		t.Error("the operator credential was replayed to the redirect target")
	}
}

// TestNewAdminClientSetsRedirectPolicy asserts the constructor — not just a
// hand-built client — installs the policy, so real CLI invocations are covered.
func TestNewAdminClientSetsRedirectPolicy(t *testing.T) {
	t.Setenv("MASTER_KEY", "sentinel-not-a-real-credential-0003")
	c := NewAdminClient("http://127.0.0.1:1", "")
	if c.HTTPClient == nil {
		t.Fatal("NewAdminClient returned a nil HTTPClient")
	}
	if c.HTTPClient.CheckRedirect == nil {
		t.Fatal("NewAdminClient did not install a CheckRedirect policy; a redirect would replay the operator credential")
	}
	if err := c.HTTPClient.CheckRedirect(nil, nil); err != http.ErrUseLastResponse {
		t.Errorf("CheckRedirect = %v, want http.ErrUseLastResponse", err)
	}
}

// TestAdminClientTreatsRedirectAsFailure pins REG-002.
//
// The status gate used to be `>= 400`, so a surfaced 3xx fell through to the
// body decode — which a BODYLESS redirect skips entirely — and do() returned
// nil. Go emits no body for a redirect on any non-GET/HEAD method, so every
// POST/PUT/DELETE admin call silently "succeeded": `keys revoke` printed
// "Key revoked." and exited 0 while the key stayed live.
//
// Each subtest is a shape the previous gate let through.
func TestAdminClientTreatsRedirectAsFailure(t *testing.T) {
	newClient := func(t *testing.T, status int, body string) *AdminClient {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Location", "http://127.0.0.1:1/elsewhere")
			w.WriteHeader(status)
			if body != "" {
				if _, err := w.Write([]byte(body)); err != nil {
					t.Errorf("write body: %v", err)
				}
			}
		}))
		t.Cleanup(srv.Close)
		return &AdminClient{
			BaseURL: srv.URL,
			APIKey:  "sentinel-not-a-real-credential-0004",
			HTTPClient: &http.Client{
				Timeout: 5 * time.Second,
				CheckRedirect: func(*http.Request, []*http.Request) error {
					return http.ErrUseLastResponse
				},
			},
		}
	}

	t.Run("bodyless 301 on GET with a destination", func(t *testing.T) {
		var out struct {
			Keys []string `json:"keys"`
		}
		if err := newClient(t, http.StatusMovedPermanently, "").Get(t.Context(), "/admin/keys", &out); err == nil {
			t.Error("GET returned nil on a bodyless 301; a redirect is not success")
		}
	})

	t.Run("bodyless 302 on POST with no destination", func(t *testing.T) {
		// The `keys revoke` shape: dest == nil, so the decode is skipped and the
		// old gate returned nil unconditionally.
		if err := newClient(t, http.StatusFound, "").Post(t.Context(), "/admin/keys/k1/revoke", nil, nil); err == nil {
			t.Error("POST returned nil on a bodyless 302; `keys revoke` would print \"Key revoked.\" while the key stays live")
		}
	})

	t.Run("307 with a JSON body", func(t *testing.T) {
		var out map[string]any
		if err := newClient(t, http.StatusTemporaryRedirect, `{"ok":true}`).Put(t.Context(), "/admin/config", map[string]any{"a": 1}, &out); err == nil {
			t.Error("PUT returned nil on a 307 carrying decodable JSON; a redirect is not success")
		}
	})

	t.Run("2xx still succeeds", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if _, err := w.Write([]byte(`{"ok":true}`)); err != nil {
				t.Errorf("write body: %v", err)
			}
		}))
		defer srv.Close()
		c := &AdminClient{BaseURL: srv.URL, APIKey: "s", HTTPClient: &http.Client{Timeout: 5 * time.Second}}
		var out map[string]any
		if err := c.Get(t.Context(), "/admin/keys", &out); err != nil {
			t.Errorf("a 200 must still succeed, got %v", err)
		}
		if out["ok"] != true {
			t.Errorf("2xx body not decoded: %v", out)
		}
	})
}

// TestAdminClientRedirectErrorNamesTarget pins REG-004 for the CLI.
//
// The realistic cause is an ingress that redirects http to https: before
// RU-01 the CLI followed it transparently, and a bare "HTTP 308" gives the
// operator nothing pointing at FERROGW_URL's scheme as the fix.
func TestAdminClientRedirectErrorNamesTarget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://gw.example.com"+r.URL.Path, http.StatusPermanentRedirect)
	}))
	defer srv.Close()

	c := &AdminClient{
		BaseURL: srv.URL,
		APIKey:  "sentinel-not-a-real-credential-0005",
		HTTPClient: &http.Client{
			Timeout: 5 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}

	err := c.Post(t.Context(), "/admin/keys", map[string]any{"name": "k"}, nil)
	if err == nil {
		t.Fatal("Post returned nil on a 308")
	}
	for _, want := range []string{"308", "https://gw.example.com"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}
