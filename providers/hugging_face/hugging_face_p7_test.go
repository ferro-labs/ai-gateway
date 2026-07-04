package huggingface

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// TestNewHuggingFace_RejectsInvalidBaseURL locks in base-URL validation.
func TestNewHuggingFace_RejectsInvalidBaseURL(t *testing.T) {
	if _, err := New("k", "://nope"); err == nil {
		t.Fatal("New accepted an invalid base URL")
	}
}

// TestEmbed_StripsV1FromTaskRoute verifies routerRoot strips a "/v1"-suffixed
// base URL so the feature-extraction task route is built off the router root
// (this is the production path — the default base URL ends in "/v1").
func TestEmbed_StripsV1FromTaskRoute(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[0.1,0.2,0.3]`)
	}))
	defer srv.Close()

	p, err := New("k", srv.URL+"/v1")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := p.Embed(context.Background(), core.EmbeddingRequest{
		Model: "sentence-transformers/all-MiniLM-L6-v2",
		Input: "hello",
	}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	want := "/hf-inference/models/sentence-transformers/all-MiniLM-L6-v2/pipeline/feature-extraction"
	if gotPath != want {
		t.Errorf("task path = %q, want %q (the /v1 must be stripped)", gotPath, want)
	}
}

// TestEmbed_EscapesModelPath verifies special characters in the model id are
// percent-escaped (so they can't alter the request path) while the owner/name
// slash is preserved.
func TestEmbed_EscapesModelPath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[0.1]`)
	}))
	defer srv.Close()

	p, _ := New("k", srv.URL+"/v1")
	_, _ = p.Embed(context.Background(), core.EmbeddingRequest{Model: "owner/bad#name", Input: "hi"})
	// "#" must be escaped so it doesn't become a fragment and drop the path tail.
	if want := "/hf-inference/models/owner/bad%23name/pipeline/feature-extraction"; gotPath != want {
		t.Errorf("escaped path = %q, want %q", gotPath, want)
	}
}

// failOnRequest returns an httptest server that fails the test if any request
// reaches it, so a validation test passes only when local validation
// short-circuits before the network (not because a real call errored out).
func failOnRequest(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected outbound request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
}

// TestGenerateImage_RejectsNonSingleN locks in that any explicit n != 1 is
// rejected by local validation before any request is sent (HF yields exactly one
// image per request); nil n is allowed.
func TestGenerateImage_RejectsNonSingleN(t *testing.T) {
	srv := failOnRequest(t)
	defer srv.Close()
	p, _ := New("k", srv.URL)
	for _, n := range []int{2, 0, -1} {
		if _, err := p.GenerateImage(context.Background(), core.ImageRequest{Model: "owner/model", Prompt: "a cat", N: &n}); err == nil {
			t.Errorf("GenerateImage accepted n=%d, want error", n)
		}
	}
}

// TestEmbed_RejectsTraversalModel locks in that dot segments in the model id are
// rejected by local validation before any request is sent.
func TestEmbed_RejectsTraversalModel(t *testing.T) {
	srv := failOnRequest(t)
	defer srv.Close()
	p, _ := New("k", srv.URL)
	if _, err := p.Embed(context.Background(), core.EmbeddingRequest{Model: "../../secret", Input: "hi"}); err == nil {
		t.Fatal("Embed accepted a traversal model path, want error")
	}
}
