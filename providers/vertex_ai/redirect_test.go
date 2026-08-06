package vertexai

import (
	"net/http"
	"testing"

	"golang.org/x/oauth2"
)

// TestTokenContextCarriesNoRedirectClient pins SEC-008.
//
// golang.org/x/oauth2 resolves its HTTP client from the context and falls back
// to http.DefaultClient when the context carries none. DefaultClient follows
// redirects, and a 307/308 preserves method and body — so the credential POSTed
// to the token endpoint would be replayed to whatever host the response names.
//
// This covers the credentials-JSON branches (service-account assertion,
// authorized_user, external_account, impersonation), which all read the client
// off the context. The GCE metadata-server branch is out of scope: it is
// ctx-free and uses the metadata package's own client, and nothing leaks there
// because the request carries only Metadata-Flavor and the token arrives in the
// response. See the tokenHTTPContext godoc.
//
// Evidence note: unlike SEC-007 this is a white-box assertion. Driving the real
// token exchange end-to-end needs a validly signed service-account credential,
// which this audit does not have and must not fabricate, so the seam is
// asserted instead of the round trip.
func TestTokenContextCarriesNoRedirectClient(t *testing.T) {
	ctx := tokenHTTPContext()

	v := ctx.Value(oauth2.HTTPClient)
	if v == nil {
		t.Fatal("token context carries no oauth2.HTTPClient; oauth2 would fall back to http.DefaultClient, which follows redirects")
	}
	c, ok := v.(*http.Client)
	if !ok {
		t.Fatalf("oauth2.HTTPClient value is %T, want *http.Client", v)
	}
	if c == http.DefaultClient {
		t.Fatal("token context carries http.DefaultClient, which follows redirects")
	}
	if c.CheckRedirect == nil {
		t.Fatal("the token-exchange client declares no redirect policy; a 307/308 would replay the signed assertion cross-host")
	}
	if err := c.CheckRedirect(nil, nil); err != http.ErrUseLastResponse {
		t.Errorf("CheckRedirect = %v, want http.ErrUseLastResponse", err)
	}
}
