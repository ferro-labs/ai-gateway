package core

import "testing"

// TestResolveAPIRoot covers the three shapes an operator can write, and the
// parts of the configured URL that must survive being rebuilt into a root.
func TestResolveAPIRoot(t *testing.T) {
	const openAIRoot = "https://api.openai.com/v1"

	tests := []struct {
		name        string
		configured  string
		defaultRoot string
		want        string
	}{
		{"unset takes the default", "", openAIRoot, openAIRoot},
		{"unset trims the default's trailing slash", "", "https://h/v1/", "https://h/v1"},

		// A bare host is not an API root, so it adopts the version segment the
		// provider's own default ends in.
		{"bare host", "http://h:9911", openAIRoot, "http://h:9911/v1"},
		{"bare host trailing slash", "http://h:9911/", openAIRoot, "http://h:9911/v1"},
		{"bare host repeated slashes", "http://h:9911///", openAIRoot, "http://h:9911/v1"},
		{"segment comes from the provider, not a constant", "http://h", "https://g.example/v1beta", "http://h/v1beta"},
		{"nested default still yields its own last segment", "http://h", "https://api.groq.com/openai/v1", "http://h/v1"},
		{"unversioned default appends nothing", "http://h", "https://api.perplexity.ai", "http://h"},

		// Anything carrying a path is the operator's own root, used verbatim.
		{"explicit v1", "https://h/v1", openAIRoot, "https://h/v1"},
		{"explicit v1 trailing slash", "https://h/v1/", openAIRoot, "https://h/v1"},
		{"mounted under a prefix", "https://h/openai/v1", openAIRoot, "https://h/openai/v1"},
		{"versionless mount", "https://h/openai", openAIRoot, "https://h/openai"},
		{"scheme case is preserved", "HTTPS://h/v1", openAIRoot, "HTTPS://h/v1"},
		{"deployment path", "https://h/deployments/d", openAIRoot, "https://h/deployments/d"},

		// Userinfo is how a base URL points at a proxy that authenticates with
		// HTTP Basic. It belongs to the host, so the path-less rebuild has to
		// carry it: dropping it turns an authenticated egress path into an
		// anonymous one, and the provider then 401s against a proxy the operator
		// gave working credentials for. Providers that set no Authorization
		// header of their own — anthropic, gemini — are the ones that notice,
		// since net/http only injects Basic into a request that has none.
		{"userinfo on a bare host", "http://u:p@h:9911", openAIRoot, "http://u:p@h:9911/v1"},
		{"userinfo with no password", "http://u@h:9911", openAIRoot, "http://u@h:9911/v1"},
		{"userinfo with an unversioned default", "http://u:p@h", "https://api.perplexity.ai", "http://u:p@h"},
		{"userinfo on a pathed base is already verbatim", "https://u:p@h/v1", openAIRoot, "https://u:p@h/v1"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveAPIRoot("p", tc.configured, tc.defaultRoot)
			if err != nil {
				t.Fatalf("ResolveAPIRoot(_, %q, %q): %v", tc.configured, tc.defaultRoot, err)
			}
			if got != tc.want {
				t.Errorf("ResolveAPIRoot(_, %q, %q) = %q, want %q", tc.configured, tc.defaultRoot, got, tc.want)
			}
		})
	}
}

// TestResolveAPIRootRejectsUnusableBases covers the bases no reading of which
// produces the request the operator meant.
//
// A query or fragment is in the list because an operation path is appended to
// whatever this returns: "https://h/v1?a=b" + "/chat/completions" asks for /v1
// with the operation buried in a query value. Refusing at construction is the
// only answer that neither invents a URL nor quietly deletes what was written.
func TestResolveAPIRootRejectsUnusableBases(t *testing.T) {
	for _, base := range []string{
		"not-a-url", "ftp://host/v1", "https://", "/v1", "://bad",
		"https://h/v1?a=b", "https://h/v1#frag", "http://h:9911?x=1", "http://h:9911/#frag",
		// A bare trailing "#" parses to an empty Fragment, so it slips past a
		// u.Fragment check and would corrupt an appended operation path.
		"https://h/v1#", "http://h:9911#",
	} {
		if _, err := ResolveAPIRoot("p", base, "https://api.openai.com/v1"); err == nil {
			t.Errorf("ResolveAPIRoot(_, %q, _) = nil error, want rejection", base)
		}
	}
}
