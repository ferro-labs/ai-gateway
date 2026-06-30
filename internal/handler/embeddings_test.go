package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	aigateway "github.com/ferro-labs/ai-gateway"
)

// TestEmbeddings_NoCapableProvider_Returns404 verifies that a request whose
// model has no registered EmbeddingProvider returns HTTP 404 with an OpenAI
// invalid_request_error/model_not_found body rather than 500/routing_error.
// Regression test for the capability-miss-as-server-error bug.
func TestEmbeddings_NoCapableProvider_Returns404(t *testing.T) {
	gw, err := aigateway.New(aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
		Targets:  []aigateway.Target{{VirtualKey: "unused"}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = gw.Close() })

	body := `{"model":"no-such-embedding-model","input":"hello"}`
	r := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(body))
	w := httptest.NewRecorder()

	Embeddings(gw)(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d (body=%s)", w.Code, w.Body.String())
	}

	var resp struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error.Type != "invalid_request_error" {
		t.Errorf("expected type invalid_request_error, got %q", resp.Error.Type)
	}
	if resp.Error.Code != "model_not_found" {
		t.Errorf("expected code model_not_found, got %q", resp.Error.Code)
	}
}
