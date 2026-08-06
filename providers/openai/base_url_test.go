package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// captureServer answers every path with an OpenAI-shaped success payload and
// records the path it was asked for, so a mis-built URL shows up as a path
// rather than as an opaque error.
type captureServer struct {
	*httptest.Server
	mu    sync.Mutex
	paths []string
}

func newCaptureServer(t *testing.T) *captureServer {
	t.Helper()
	c := &captureServer{}
	c.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.paths = append(c.paths, r.URL.Path)
		c.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "chat/completions"):
			_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion","created":1,"model":"gpt-4o-mini","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
		case strings.HasSuffix(r.URL.Path, "embeddings"):
			_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1]}],"model":"text-embedding-3-small","usage":{"prompt_tokens":1,"total_tokens":1}}`))
		case strings.HasSuffix(r.URL.Path, "images/generations"):
			_, _ = w.Write([]byte(`{"created":1,"data":[{"url":"http://example.invalid/i.png"}]}`))
		case strings.HasSuffix(r.URL.Path, "models"):
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-4o-mini","object":"model","created":1,"owned_by":"stub"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"no handler","type":"invalid_request_error"}}`))
		}
	}))
	t.Cleanup(c.Close)
	return c
}

func (c *captureServer) captured() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := append([]string(nil), c.paths...)
	sort.Strings(out)
	return out
}

// exerciseEverySurface drives each surface the provider serves against the
// capture server. Errors are deliberately ignored: the assertion is about the
// URL that was requested, and a surface that fails still reveals its path.
func exerciseEverySurface(t *testing.T, p *Provider) {
	t.Helper()
	ctx := context.Background()

	_, _ = p.Complete(ctx, core.Request{
		Model:    "gpt-4o-mini",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	})
	_, _ = p.Embed(ctx, core.EmbeddingRequest{Model: "text-embedding-3-small", Input: "hi"})
	_, _ = p.GenerateImage(ctx, core.ImageRequest{Model: "dall-e-3", Prompt: "a cat"})
	_, _ = p.DiscoverModels(ctx)
}

// TestBaseURL_EverySurfaceSharesOneRoot is the regression guard for the split
// this provider carried: chat built its URL by hand and appended /v1 when the
// base lacked it, while embeddings and images went through the SDK, which
// treats the base as the API root. One configured value produced two
// conventions, so a base without the suffix served chat and 404'd the rest.
//
// Every base shape below must send every surface to the SAME root.
func TestBaseURL_EverySurfaceSharesOneRoot(t *testing.T) {
	srv := newCaptureServer(t)

	tests := []struct {
		name     string
		base     string
		wantRoot string
	}{
		// A bare host is not an API root — no OpenAI-wire server serves the API
		// there — so it is resolved to one. This is the shape that broke.
		{name: "bare host", base: srv.URL, wantRoot: "/v1"},
		{name: "bare host trailing slash", base: srv.URL + "/", wantRoot: "/v1"},
		// A base carrying a path is the operator's own root, used verbatim.
		{name: "explicit v1", base: srv.URL + "/v1", wantRoot: "/v1"},
		{name: "explicit v1 trailing slash", base: srv.URL + "/v1/", wantRoot: "/v1"},
		{name: "mounted under a prefix", base: srv.URL + "/openai/v1", wantRoot: "/openai/v1"},
		{name: "versionless mount", base: srv.URL + "/openai", wantRoot: "/openai"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv.mu.Lock()
			srv.paths = nil
			srv.mu.Unlock()

			p, err := New("sk-test", tc.base)
			if err != nil {
				t.Fatalf("New(%q): %v", tc.base, err)
			}
			exerciseEverySurface(t, p)

			got := srv.captured()
			want := []string{
				tc.wantRoot + "/chat/completions",
				tc.wantRoot + "/embeddings",
				tc.wantRoot + "/images/generations",
				tc.wantRoot + "/models",
			}
			sort.Strings(want)
			if len(got) != len(want) {
				t.Fatalf("captured %v, want %v", got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Errorf("captured %v, want %v", got, want)
					break
				}
			}
		})
	}
}

// TestBaseURL_DefaultIsTheAPIRoot pins the default to the same string every
// official OpenAI client uses, so the no-override path follows the same rule as
// the override path.
func TestBaseURL_DefaultIsTheAPIRoot(t *testing.T) {
	p, err := New("sk-test", "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := p.BaseURL(); got != "https://api.openai.com/v1" {
		t.Errorf("BaseURL() = %q, want %q", got, "https://api.openai.com/v1")
	}
}

// TestBaseURL_ResolvedRoot covers the shapes the live surfaces above cannot
// reach, including the userinfo a base URL carries when it points at a proxy
// that authenticates with HTTP Basic.
func TestBaseURL_ResolvedRoot(t *testing.T) {
	tests := map[string]string{ //nolint:gosec // G101: "u:p" is the userinfo shape under test, not a credential
		"http://h:9911":           "http://h:9911/v1",
		"http://h:9911/":          "http://h:9911/v1",
		"http://u:p@h:9911":       "http://u:p@h:9911/v1",
		"http://u:p@h:9911/v1":    "http://u:p@h:9911/v1",
		"https://h/v1":            "https://h/v1",
		"https://h/v1/":           "https://h/v1",
		"https://h/openai/v1":     "https://h/openai/v1",
		"https://h/openai":        "https://h/openai",
		"HTTPS://h/v1":            "HTTPS://h/v1",
		"https://h/deployments/d": "https://h/deployments/d",
	}
	for base, want := range tests {
		t.Run(base, func(t *testing.T) {
			p, err := New("sk-test", base)
			if err != nil {
				t.Fatalf("New(_, %q): %v", base, err)
			}
			if got := p.BaseURL(); got != want {
				t.Errorf("BaseURL() = %q, want %q", got, want)
			}
		})
	}
}

func TestBaseURL_RejectsUnusableBases(t *testing.T) {
	// A query or fragment is refused rather than dropped: an operation path is
	// appended to whatever the root resolves to, so neither can survive, and
	// deleting what the operator wrote is the silent half of the same defect.
	for _, base := range []string{
		"not-a-url", "ftp://host/v1", "https://", "/v1",
		"http://h:9911?x=1", "http://h:9911/#frag", "https://h/v1?a=b",
	} {
		if _, err := New("sk-test", base); err == nil {
			t.Errorf("New(_, %q) = nil error, want rejection", base)
		}
	}
}
