package httpclient

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// redirectSentinel is a synthetic credential. It is never a real key; it exists
// only so a test can assert it does NOT reach a second host.
const redirectSentinel = "sentinel-not-a-real-credential-0001"

// newRedirectChain starts two servers: `first` answers every request with a 302
// to `second`, and `second` records the headers it was handed. It returns the
// first server's URL and a func reporting what (if anything) reached `second`.
func newRedirectChain(t *testing.T) (firstURL string, secondSaw func() (hit bool, auth, apiKey string)) {
	t.Helper()

	var (
		hit    bool
		auth   string
		apiKey string
	)
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		auth = r.Header.Get("Authorization")
		apiKey = r.Header.Get("X-Api-Key")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(second.Close)

	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, second.URL+r.URL.Path, http.StatusFound)
	}))
	t.Cleanup(first.Close)

	return first.URL, func() (bool, string, string) { return hit, auth, apiKey }
}

// assertDoesNotFollow drives one client through the redirect chain and asserts
// the 302 is surfaced rather than followed, and that no credential reached the
// redirect target.
func assertDoesNotFollow(t *testing.T, name string, c *http.Client) {
	t.Helper()

	firstURL, secondSaw := newRedirectChain(t)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, firstURL+"/v1/models", nil)
	if err != nil {
		t.Fatalf("%s: new request: %v", name, err)
	}
	// Both header shapes matter: Go strips Authorization on a cross-host hop but
	// copies custom headers verbatim, which is how a provider key escapes.
	req.Header.Set("Authorization", "Bearer "+redirectSentinel)
	req.Header.Set("X-Api-Key", redirectSentinel)

	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("%s: do request: %v", name, err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			t.Errorf("%s: close body: %v", name, closeErr)
		}
	}()

	if resp.StatusCode != http.StatusFound {
		t.Errorf("%s: status = %d, want 302 surfaced to the caller (redirect must not be followed)", name, resp.StatusCode)
	}
	hit, auth, apiKey := secondSaw()
	if hit {
		t.Errorf("%s: the redirect target was contacted; the client followed the 302", name)
	}
	if auth != "" || apiKey != "" {
		t.Errorf("%s: credential replayed to the redirect target (Authorization set=%t, X-Api-Key set=%t)",
			name, auth != "", apiKey != "")
	}
}

// TestClientsDoNotFollowRedirects pins SEC-002 (absorbing MCP-002): an outbound
// client that injects a provider or MCP credential must not follow an upstream
// redirect, because Go replays custom auth headers such as x-api-key on a
// cross-host hop.
func TestClientsDoNotFollowRedirects(t *testing.T) {
	assertDoesNotFollow(t, "New(timeout)", New(5*time.Second))
	assertDoesNotFollow(t, "New(0)/shared default", New(0))
	assertDoesNotFollow(t, "ForProvider", ForProvider("openai"))
	assertDoesNotFollow(t, "ForProvider/unknown", ForProvider("not-a-registered-provider"))
}

// TestEveryOutboundClientDeclaresARedirectPolicy fails when a new
// http.Client is constructed in production code without CheckRedirect.
//
// The four clients that existed when SEC-002 was fixed all inject a credential
// of some kind, and the next one added is overwhelmingly likely to as well. A
// behavioural test can only cover the clients it knows about, so this guard
// covers the ones nobody has written yet.
func TestEveryOutboundClientDeclaresARedirectPolicy(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}

	var offenders []string
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "docs", "web", "dist":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		//nolint:gosec // G304: path comes from walking this repository's own
		// source tree in a test, not from user input.
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		text := string(src)
		for idx := 0; ; {
			at := strings.Index(text[idx:], "&http.Client{")
			if at < 0 {
				break
			}
			at += idx
			// Inspect the composite literal: from the brace to its match.
			depth, end := 0, len(text)
			for i := at + len("&http.Client"); i < len(text); i++ {
				switch text[i] {
				case '{':
					depth++
				case '}':
					depth--
					if depth == 0 {
						end = i
						i = len(text)
					}
				}
			}
			if !strings.Contains(text[at:end], "CheckRedirect") {
				rel, relErr := filepath.Rel(root, path)
				if relErr != nil {
					rel = path
				}
				line := 1 + strings.Count(text[:at], "\n")
				offenders = append(offenders, rel+":"+strconv.Itoa(line))
			}
			idx = at + len("&http.Client{")
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk repo: %v", walkErr)
	}

	if len(offenders) > 0 {
		t.Errorf("http.Client constructed without a CheckRedirect policy at %v; "+
			"an outbound client that carries a credential must surface redirects rather than follow them (SEC-002)",
			offenders)
	}
}
