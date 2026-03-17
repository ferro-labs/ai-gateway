package cloudflare

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

const testBearerAPIKey = "Bearer test-key"

func TestNewCloudflare(t *testing.T) {
	p, err := New("test-key", "acct-123", "")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if p.Name() != "cloudflare" {
		t.Errorf("Name() = %q, want cloudflare", p.Name())
	}
	if got := p.BaseURL(); got != "https://api.cloudflare.com/client/v4/accounts/acct-123/ai/v1" {
		t.Errorf("BaseURL() = %q", got)
	}
}

func TestCloudflareProvider_SupportedModels(t *testing.T) {
	p, _ := New("test-key", "acct-123", "")
	models := p.SupportedModels()
	if len(models) == 0 {
		t.Error("SupportedModels() returned empty")
	}
	found := false
	for _, m := range models {
		if m == "@cf/meta/llama-3.1-8b-instruct" {
			found = true
		}
	}
	if !found {
		t.Error("@cf/meta/llama-3.1-8b-instruct not found")
	}
}

func TestCloudflareProvider_SupportsModel(t *testing.T) {
	p, _ := New("test-key", "acct-123", "")
	if !p.SupportsModel("@cf/meta/llama-3.1-8b-instruct") {
		t.Error("expected model to be supported")
	}
	if !p.SupportsModel("@cf/custom/model") {
		t.Error("passthrough: expected all models to return true")
	}
}

func TestCloudflareProvider_Models(t *testing.T) {
	p, _ := New("test-key", "acct-123", "")
	models := p.Models()
	for _, m := range models {
		if m.OwnedBy != "cloudflare" {
			t.Errorf("ModelInfo.OwnedBy = %q, want cloudflare", m.OwnedBy)
		}
	}
}

func TestCloudflareProvider_Interfaces(_ *testing.T) {
	p, _ := New("test-key", "acct-123", "")
	var _ core.StreamProvider = p
	var _ core.EmbeddingProvider = p
}

func TestCloudflareProvider_AuthHeaders(t *testing.T) {
	p, _ := New("test-key", "acct-123", "")
	headers := p.AuthHeaders()
	if headers["Authorization"] != testBearerAPIKey {
		t.Errorf("AuthHeaders Authorization = %q, want %s", headers["Authorization"], testBearerAPIKey)
	}
}

func TestCloudflareProvider_CompleteStream_MockSSE(t *testing.T) {
	sseData := "data: {\"id\":\"cmpl-1\",\"model\":\"@cf/meta/llama-3.1-8b-instruct\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"cmpl-1\",\"model\":\"@cf/meta/llama-3.1-8b-instruct\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"cmpl-1\",\"model\":\"@cf/meta/llama-3.1-8b-instruct\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" there\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"cmpl-1\",\"model\":\"@cf/meta/llama-3.1-8b-instruct\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			t.Fatalf("path = %q, want suffix /chat/completions", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseData))
	}))
	defer srv.Close()

	p, _ := New("test-key", "", srv.URL)
	ch, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "@cf/meta/llama-3.1-8b-instruct",
		Messages: []core.Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("CompleteStream() error: %v", err)
	}

	var chunks []core.StreamChunk
	for c := range ch {
		chunks = append(chunks, c)
	}

	if len(chunks) < 3 {
		t.Fatalf("expected at least 3 chunks, got %d", len(chunks))
	}
	if chunks[1].Choices[0].Delta.Content != "Hello" {
		t.Errorf("delta content = %q, want Hello", chunks[1].Choices[0].Delta.Content)
	}
	if chunks[2].Choices[0].Delta.Content != " there" {
		t.Errorf("delta content = %q, want ' there'", chunks[2].Choices[0].Delta.Content)
	}
}

func TestCloudflareProvider_Complete_MockHTTP(t *testing.T) {
	respBody := `{"id":"cmpl-1","model":"@cf/meta/llama-3.1-8b-instruct","choices":[{"index":0,"message":{"role":"assistant","content":"Hello!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			t.Fatalf("path = %q, want suffix /chat/completions", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(respBody))
	}))
	defer srv.Close()

	p, _ := New("test-key", "", srv.URL)
	resp, err := p.Complete(context.Background(), core.Request{
		Model:    "@cf/meta/llama-3.1-8b-instruct",
		Messages: []core.Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if resp.ID != "cmpl-1" {
		t.Errorf("Response.ID = %q, want cmpl-1", resp.ID)
	}
}

func TestCloudflareProvider_Embed_MockHTTP(t *testing.T) {
	embedResp := `{"object":"list","data":[{"object":"embedding","embedding":[0.1,0.2],"index":0}],"model":"@cf/baai/bge-large-en-v1.5","usage":{"prompt_tokens":2,"total_tokens":2}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/embeddings") {
			t.Fatalf("path = %q, want suffix /embeddings", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(embedResp))
	}))
	defer srv.Close()

	p, _ := New("test-key", "", srv.URL)
	resp, err := p.Embed(context.Background(), core.EmbeddingRequest{
		Model: "@cf/baai/bge-large-en-v1.5",
		Input: "hello",
	})
	if err != nil {
		t.Fatalf("Embed() error: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("Data length = %d, want 1", len(resp.Data))
	}
}
