package transport

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// redirectTargetOf describes the shape a refused redirect reaches a caller as —
// the 3xx itself, with resp.Request set the way http.Client sets it — and
// returns what RedirectTarget makes of it.
func redirectTargetOf(t *testing.T, status int, requestURL, location string) string {
	t.Helper()
	u, err := url.Parse(requestURL)
	if err != nil {
		t.Fatalf("parse request url: %v", err)
	}
	h := http.Header{}
	if location != "" {
		h.Set("Location", location)
	}
	return RedirectTarget(&http.Response{
		StatusCode: status,
		Header:     h,
		Request:    &http.Request{Method: http.MethodGet, URL: u},
	})
}

func TestRedirectTarget(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		request  string
		location string
		want     string
	}{{
		name:     "cross-host names the target",
		status:   http.StatusFound,
		request:  "https://api.example.com/v1/models",
		location: "https://elsewhere.example.net/v1/models",
		want:     "redirect to https://elsewhere.example.net",
	}, {
		name:     "same-host trailing slash is called out as same host",
		status:   http.StatusTemporaryRedirect,
		request:  "http://127.0.0.1:9000/mcp",
		location: "/mcp/",
		want:     "redirect to http://127.0.0.1:9000 (same host)",
	}, {
		name:     "scheme upgrade is a different host and names the scheme",
		status:   http.StatusPermanentRedirect,
		request:  "http://gw.example.com:80/admin/keys",
		location: "https://gw.example.com/admin/keys",
		want:     "redirect to https://gw.example.com",
	}, {
		name:     "absent Location is reported rather than hidden",
		status:   http.StatusFound,
		request:  "https://api.example.com/v1/models",
		location: "",
		want:     "redirect with no Location header",
	}, {
		name:     "non-redirect yields nothing so callers fall through",
		status:   http.StatusBadGateway,
		request:  "https://api.example.com/v1/models",
		location: "https://elsewhere.example.net/",
		want:     "",
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := redirectTargetOf(t, tc.status, tc.request, tc.location); got != tc.want {
				t.Errorf("RedirectTarget() = %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("nil response", func(t *testing.T) {
		if got := RedirectTarget(nil); got != "" {
			t.Errorf("RedirectTarget(nil) = %q, want empty", got)
		}
	})
}

// TestRedirectTargetOmitsCredentialBearingParts is the security half: a Location
// is attacker- or misconfiguration-supplied and can carry a credential in its
// userinfo or its query string. Naming the host must never quote either.
func TestRedirectTargetOmitsCredentialBearingParts(t *testing.T) {
	const (
		userinfoSecret = "sentinel-not-a-real-credential-0101"
		querySecret    = "sentinel-not-a-real-credential-0102"
	)
	got := redirectTargetOf(t, http.StatusFound,
		"https://api.example.com/v1/models",
		"https://admin:"+userinfoSecret+"@elsewhere.example.net/v1/models?api_key="+querySecret+"#frag",
	)
	if got != "redirect to https://elsewhere.example.net" {
		t.Errorf("RedirectTarget() = %q, want the scheme and host alone", got)
	}
	for _, leak := range []string{userinfoSecret, querySecret, "admin:", "api_key", "/v1/models", "frag"} {
		if strings.Contains(got, leak) {
			t.Errorf("RedirectTarget() leaked %q into %q", leak, got)
		}
	}
}
