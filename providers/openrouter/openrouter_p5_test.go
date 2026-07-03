package openrouter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

const testEmbeddingModel = "text-embedding-3-small"

// TestOpenRouterProvider_Embed_Interface guards the compile-time contract.
func TestOpenRouterProvider_Embed_Interface(_ *testing.T) {
	p, _ := New("test-key", "")
	var _ core.EmbeddingProvider = p
}

// TestOpenRouterProvider_Embed_MockHTTP asserts the embedding request targets
// POST /embeddings with Bearer auth and returns the decoded embedding.
func TestOpenRouterProvider_Embed_MockHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/embeddings" {
			t.Errorf("path = %q, want /embeddings", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != testBearerAPIKey {
			t.Errorf("Authorization = %q, want %s", got, testBearerAPIKey)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["model"] != testEmbeddingModel {
			t.Errorf("model = %v, want %s", body["model"], testEmbeddingModel)
		}
		if body["input"] != "hello world" {
			t.Errorf("input = %v, want hello world", body["input"])
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","embedding":[0.1,0.2],"index":0}],"model":"` + testEmbeddingModel + `","usage":{"prompt_tokens":3,"total_tokens":3}}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	resp, err := p.Embed(context.Background(), core.EmbeddingRequest{Model: testEmbeddingModel, Input: "hello world"})
	if err != nil {
		t.Fatalf("Embed() error: %v", err)
	}
	if resp.Object != "list" || resp.Model != testEmbeddingModel {
		t.Errorf("resp = %+v, want list/%s", resp, testEmbeddingModel)
	}
	if len(resp.Data) != 1 || resp.Data[0].Index != 0 || !reflect.DeepEqual(resp.Data[0].Embedding, []float64{0.1, 0.2}) {
		t.Errorf("Data = %+v, want one embedding at index 0", resp.Data)
	}
	if resp.Usage.PromptTokens != 3 || resp.Usage.TotalTokens != 3 {
		t.Errorf("Usage = %+v, want prompt=3 total=3", resp.Usage)
	}
}

// TestOpenRouterProvider_Embed_InvalidEncodingFormat rejects unsupported
// encoding formats before any network call.
func TestOpenRouterProvider_Embed_InvalidEncodingFormat(t *testing.T) {
	p, _ := New("test-key", "http://127.0.0.1:0")
	_, err := p.Embed(context.Background(), core.EmbeddingRequest{
		Model:          testEmbeddingModel,
		Input:          "hi",
		EncodingFormat: "base64",
	})
	if err == nil {
		t.Fatal("Embed() error = nil, want unsupported encoding_format error")
	}
}

// TestOpenRouterProvider_Complete_NestedUsage exercises the shared core.Usage
// nested-usage decode: OpenAI-style prompt_tokens_details.cached_tokens and
// completion_tokens_details.reasoning_tokens must surface on core.Usage.
func TestOpenRouterProvider_Complete_NestedUsage(t *testing.T) {
	respBody := `{"id":"cmpl-1","model":"openrouter/auto","choices":[{"index":0,"message":{"role":"assistant","content":"Hello!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":30,"completion_tokens":20,"total_tokens":50,"prompt_tokens_details":{"cached_tokens":8},"completion_tokens_details":{"reasoning_tokens":12}}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(respBody))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	resp, err := p.Complete(context.Background(), core.Request{
		Model:    "openrouter/auto",
		Messages: []core.Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if resp.Usage.ReasoningTokens != 12 {
		t.Errorf("Usage.ReasoningTokens = %d, want 12", resp.Usage.ReasoningTokens)
	}
	if resp.Usage.CacheReadTokens != 8 {
		t.Errorf("Usage.CacheReadTokens = %d, want 8", resp.Usage.CacheReadTokens)
	}
}

// TestOpenRouterProvider_Complete_ErrorPath asserts a non-2xx chat response is
// surfaced as a core.APIError carrying the upstream status and message.
func TestOpenRouterProvider_Complete_ErrorPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	_, err := p.Complete(context.Background(), core.Request{
		Model:    "openrouter/auto",
		Messages: []core.Message{{Role: "user", Content: "Hi"}},
	})
	if err == nil {
		t.Fatal("Complete() error = nil, want error for 500")
	}
	if !strings.Contains(err.Error(), "boom") || !strings.Contains(err.Error(), "500") {
		t.Errorf("error = %v, want status 500 + upstream message", err)
	}
}

// TestOpenRouterProvider_DiscoverModels asserts live model enumeration hits
// GET /models with Bearer auth and decodes the returned list.
func TestOpenRouterProvider_DiscoverModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		if r.URL.Path != "/models" {
			t.Errorf("path = %q, want /models", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != testBearerAPIKey {
			t.Errorf("Authorization = %q, want %s", got, testBearerAPIKey)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"openrouter/auto","object":"model","created":1700000000,"owned_by":"openrouter"}]}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	models, err := p.DiscoverModels(context.Background())
	if err != nil {
		t.Fatalf("DiscoverModels() error: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(models))
	}
	if models[0].ID != "openrouter/auto" {
		t.Errorf("model[0].ID = %q, want openrouter/auto", models[0].ID)
	}
	if models[0].OwnedBy != "openrouter" {
		t.Errorf("model[0].OwnedBy = %q, want openrouter", models[0].OwnedBy)
	}
}
