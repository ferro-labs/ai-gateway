package azurefoundry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

func TestAzureFoundryProvider_Embed_Interface(_ *testing.T) {
	p, _ := New("test-key", "https://example.services.ai.azure.com", "")
	var _ core.EmbeddingProvider = p
}

func TestAzureFoundryProvider_Embed_MockHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/openai/v1/embeddings" {
			t.Errorf("path = %q, want /openai/v1/embeddings", r.URL.Path)
		}
		// Azure authenticates with the api-key header, never Bearer.
		if got := r.Header.Get("api-key"); got != "test-key" {
			t.Errorf("api-key = %q, want test-key", got)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization = %q, want empty (Azure uses api-key)", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","embedding":[0.1,0.2],"index":0}],"model":"text-embedding-3-small","usage":{"prompt_tokens":3,"total_tokens":3}}`))
	}))
	defer srv.Close()

	p, err := New("test-key", srv.URL, "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	resp, err := p.Embed(context.Background(), core.EmbeddingRequest{Model: "text-embedding-3-small", Input: "hello world"})
	if err != nil {
		t.Fatalf("Embed() error: %v", err)
	}
	if resp.Object != "list" || resp.Model != "text-embedding-3-small" || len(resp.Data) != 1 {
		t.Errorf("resp = %+v, want list/text-embedding-3-small/1 datum", resp)
	}
	if !reflect.DeepEqual(resp.Data[0].Embedding, []float64{0.1, 0.2}) {
		t.Errorf("embedding = %+v, want [0.1 0.2]", resp.Data[0].Embedding)
	}
	if resp.Usage.TotalTokens != 3 {
		t.Errorf("total tokens = %d, want 3", resp.Usage.TotalTokens)
	}
}

func TestAzureFoundryProvider_Embed_InvalidEncodingFormat(t *testing.T) {
	p, _ := New("test-key", "https://example.services.ai.azure.com", "")
	_, err := p.Embed(context.Background(), core.EmbeddingRequest{
		Model:          "text-embedding-3-small",
		Input:          "hi",
		EncodingFormat: "base64",
	})
	if err == nil {
		t.Fatal("Embed() error = nil, want unsupported encoding_format error")
	}
}
