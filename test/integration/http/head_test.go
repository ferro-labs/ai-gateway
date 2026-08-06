//go:build integration
// +build integration

package http_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// RFC 9110 §9.3.2 defines HEAD as GET with the body omitted, and HEAD is what
// load-balancer probes and cache revalidation send. Every GET route the gateway
// owns must therefore answer it with the GET status and the GET headers, and no
// body — including the routes behind auth, where a probe that presents no
// credential must still be refused rather than served.
func TestHead_MirrorsGetOnEveryGetRoute(t *testing.T) {
	env := newTestServer(t)

	for _, tc := range []struct {
		path      string
		authorize bool
	}{
		{"/livez", false},
		{"/readyz", false},
		{"/health", false},
		{"/v1/models", true},
		{"/v1/capabilities", true},
		{"/metrics", true},
	} {
		t.Run(tc.path, func(t *testing.T) {
			do := func(method string) *http.Response {
				req := newTestRequest(t, method, env.Server.URL+tc.path, nil)
				if tc.authorize {
					req.Header.Set("Authorization", "Bearer "+testMasterKey)
				}
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatalf("%s %s: %v", method, tc.path, err)
				}
				t.Cleanup(func() { closeTestBody(t, resp.Body) })
				return resp
			}

			//nolint:bodyclose // do registers the close with t.Cleanup.
			get := do(http.MethodGet)
			getBody, err := io.ReadAll(get.Body)
			if err != nil {
				t.Fatalf("read GET body: %v", err)
			}
			if get.StatusCode != http.StatusOK {
				t.Fatalf("GET %s = %d, want 200: %s", tc.path, get.StatusCode, getBody)
			}

			//nolint:bodyclose // do registers the close with t.Cleanup.
			head := do(http.MethodHead)
			if head.StatusCode != get.StatusCode {
				t.Errorf("HEAD status = %d, want the GET status %d", head.StatusCode, get.StatusCode)
			}
			if got, want := head.Header.Get("Content-Type"), get.Header.Get("Content-Type"); got != want {
				t.Errorf("HEAD Content-Type = %q, want the GET value %q", got, want)
			}
			headBody, err := io.ReadAll(head.Body)
			if err != nil {
				t.Fatalf("read HEAD body: %v", err)
			}
			if len(headBody) != 0 {
				t.Errorf("HEAD returned a body of %d bytes: %s", len(headBody), headBody)
			}
		})
	}
}

// An authenticated route answers HEAD the same way it answers GET when no
// credential is presented: a probe must not be a way around auth.
func TestHead_OnAuthenticatedRouteStillRequiresAuth(t *testing.T) {
	env := newTestServer(t)

	for _, path := range []string{"/v1/models", "/v1/capabilities", "/metrics"} {
		t.Run(path, func(t *testing.T) {
			req := newTestRequest(t, http.MethodHead, env.Server.URL+path, nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("HEAD %s: %v", path, err)
			}
			defer closeTestBody(t, resp.Body)

			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("HEAD %s = %d, want 401", path, resp.StatusCode)
			}
		})
	}
}

// HEAD is not a method a POST route serves. It must be refused with a 405 whose
// Allow header names what the route does serve — SDKs read Allow rather than
// retrying — and it must never fall through to the /v1/* pass-through, which
// would forward it upstream under the gateway's own credential.
func TestHead_OnPostRouteIs405WithAllow(t *testing.T) {
	env := newTestServer(t)

	for _, path := range []string{
		"/v1/chat/completions",
		"/v1/completions",
		"/v1/embeddings",
		"/v1/images/generations",
	} {
		t.Run(path, func(t *testing.T) {
			req := newTestRequest(t, http.MethodHead, env.Server.URL+path, nil)
			req.Header.Set("Authorization", "Bearer "+testMasterKey)

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("HEAD %s: %v", path, err)
			}
			defer closeTestBody(t, resp.Body)

			if resp.StatusCode != http.StatusMethodNotAllowed {
				t.Fatalf("HEAD %s = %d, want 405", path, resp.StatusCode)
			}
			if allow := resp.Header.Get("Allow"); !strings.Contains(allow, http.MethodPost) {
				t.Errorf("Allow = %q, want it to name POST", allow)
			}
		})
	}
}

// A GET route's 405 for a method it does not serve names HEAD alongside GET,
// because it serves both.
func TestGetRoute_AllowNamesHead(t *testing.T) {
	env := newTestServer(t)

	req := newTestRequest(t, http.MethodPost, env.Server.URL+"/v1/capabilities", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+testMasterKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/capabilities: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", resp.StatusCode)
	}
	allow := resp.Header.Get("Allow")
	if !strings.Contains(allow, http.MethodGet) || !strings.Contains(allow, http.MethodHead) {
		t.Errorf("Allow = %q, want it to name both GET and HEAD", allow)
	}
}
