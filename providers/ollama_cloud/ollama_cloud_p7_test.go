package ollamacloud

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// TestEmbed_ForwardsDimensions verifies req.Dimensions reaches the native
// /api/embed request body rather than being silently dropped.
func TestEmbed_ForwardsDimensions(t *testing.T) {
	var gotDims *int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Dimensions *int `json:"dimensions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotDims = body.Dimensions
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"m","embeddings":[[0.1]],"prompt_eval_count":1}`))
	}))
	defer server.Close()

	p, err := New(testCloudAPIKey, server.URL, []string{testCloudModel})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	dims := 8
	if _, err := p.Embed(context.Background(), core.EmbeddingRequest{Model: "m", Input: "hi", Dimensions: &dims}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if gotDims == nil || *gotDims != 8 {
		t.Errorf("dimensions forwarded = %v, want 8", gotDims)
	}
}
