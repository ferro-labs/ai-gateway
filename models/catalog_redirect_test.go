package models

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestFetchRemoteFollowsRedirects pins REG-001.
//
// defaultCatalogURL points at a GitHub `releases/latest/download/<asset>`
// alias, which cannot be served except by redirect: GitHub resolves `latest`
// to a tag and then to the release object store. A no-redirect policy on this
// client therefore breaks the remote catalog and its periodic refresh on the
// default configuration — which is exactly what RU-01 did.
//
// The catalog client is the one outbound client that carries no credential:
// fetchRemote attaches no auth header, and its only exposure is operator-
// supplied userinfo, which cannot cross a hop (see
// TestFetchRemoteDropsUserinfoAcrossARedirect). So it must follow redirects,
// unlike the provider, MCP, discovery and CLI clients, which must not (SEC-002).
func TestFetchRemoteFollowsRedirects(t *testing.T) {
	const body = `{"openai/gpt-4o":{"provider":"openai","model_id":"gpt-4o","mode":"chat"}}`

	var reached bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("write redirect-target body: %v", err)
		}
	}))
	defer target.Close()

	// Two hops, matching GitHub's own latest -> tag -> object-store chain.
	hop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer hop.Close()

	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, hop.URL, http.StatusFound)
	}))
	defer first.Close()

	got, err := fetchRemote(t.Context(), first.URL)
	if err != nil {
		t.Fatalf("fetchRemote through a redirect chain: %v; the default catalog URL requires redirects", err)
	}
	if !reached {
		t.Error("the redirect target was never contacted")
	}
	if string(got) != body {
		t.Errorf("body = %q, want the redirect target's payload", string(got))
	}
}

// TestFetchRemoteDropsUserinfoAcrossARedirect makes the reason the redirect
// exception is safe executable, because that reason was previously recorded
// wrongly: as net/http's sensitive-header stripping, which is a
// domain-or-subdomain suffix match and would let an explicitly set
// Authorization survive a hop to a sibling host. The operative mechanism is
// net/url.(*URL).ResolveReference — an absolute Location carries its own User,
// and the relative branches copy Host and User together — so userinfo travels
// only with the host it was supplied with.
//
// The test fails if a credential is ever attached to this client, which is the
// change that would void the exception.
func TestFetchRemoteDropsUserinfoAcrossARedirect(t *testing.T) {
	const body = `{"openai/gpt-4o":{"provider":"openai","model_id":"gpt-4o","mode":"chat"}}`

	var targetAuth string
	var targetReached bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetReached = true
		targetAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("write redirect-target body: %v", err)
		}
	}))
	defer target.Close()

	var originAuth string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originAuth = r.Header.Get("Authorization")
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer origin.Close()

	withUserinfo := strings.Replace(origin.URL, "http://", "http://mirror-user:mirror-pass@", 1)
	if _, err := fetchRemote(t.Context(), withUserinfo); err != nil {
		t.Fatalf("fetchRemote with userinfo: %v", err)
	}

	// The first hop does receive it, so the assertion below is about the hop and
	// not about userinfo being inert.
	if originAuth == "" {
		t.Error("the origin received no Authorization header; userinfo is no longer sent even on the first hop, so this test proves nothing")
	}
	if !targetReached {
		t.Fatal("the redirect target was never contacted")
	}
	if targetAuth != "" {
		t.Errorf("the redirect target received Authorization %q; userinfo must not cross a hop, and no credential may be attached to this client", targetAuth)
	}
}
