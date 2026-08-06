package cloudflare

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// TestEmbed_RejectsUnsupportedEncodingFormat verifies the encoding_format guard
// the other OpenAI-compatible embedding providers already had. See the
// equivalent test in providers/sambanova for why forwarding it unguarded turned
// a caller error into a 500.
func TestEmbed_RejectsUnsupportedEncodingFormat(t *testing.T) {
	var upstreamCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[],"model":"m","usage":{}}`))
	}))
	defer srv.Close()

	p, err := New("test-key", "acct", srv.URL)
	if err != nil {
		t.Fatalf("New(): %v", err)
	}

	_, err = p.Embed(context.Background(), core.EmbeddingRequest{
		Model:          "@cf/baai/bge-base-en-v1.5",
		Input:          "hi",
		EncodingFormat: "base64",
	})
	if err == nil {
		t.Fatal("Embed() with encoding_format=base64 returned no error")
	}
	if upstreamCalls != 0 {
		t.Errorf("upstream was called %d times; the request must be refused before it is forwarded", upstreamCalls)
	}
	if got := core.ParseStatusCode(err); got != http.StatusBadRequest {
		t.Errorf("ParseStatusCode(%v) = %d, want %d", err, got, http.StatusBadRequest)
	}

	// "float" and unset are still forwarded.
	for _, ok := range []string{"", "float"} {
		if _, err := p.Embed(context.Background(), core.EmbeddingRequest{
			Model:          "@cf/baai/bge-base-en-v1.5",
			Input:          "hi",
			EncodingFormat: ok,
		}); err != nil {
			t.Errorf("Embed() with encoding_format=%q: %v", ok, err)
		}
	}
}
