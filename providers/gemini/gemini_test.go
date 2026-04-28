package gemini

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

func TestNewGemini(t *testing.T) {
	p, err := New("test-key", "")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if p.Name() != "gemini" {
		t.Errorf("Name() = %q, want gemini", p.Name())
	}
}

func TestGeminiProvider_SupportedModels(t *testing.T) {
	p, _ := New("test-key", "")
	models := p.SupportedModels()
	if len(models) == 0 {
		t.Error("SupportedModels() returned empty")
	}
	found := false
	foundEmbedding := false
	for _, m := range models {
		if m == "gemini-2.0-flash" {
			found = true
		}
		if m == "gemini-embedding-001" {
			foundEmbedding = true
		}
	}
	if !found {
		t.Error("gemini-2.0-flash not found")
	}
	if !foundEmbedding {
		t.Error("gemini-embedding-001 not found")
	}
}

func TestGeminiProvider_SupportsModel(t *testing.T) {
	p, _ := New("test-key", "")
	if !p.SupportsModel("gemini-2.0-flash") {
		t.Error("expected gemini-2.0-flash to be supported")
	}
	if p.SupportsModel("gpt-4o") {
		t.Error("gemini should not support gpt-4o")
	}
	if !p.SupportsModel("text-embedding-004") {
		t.Error("expected text-embedding-004 to be supported")
	}
}

func TestGeminiProvider_Models(t *testing.T) {
	p, _ := New("test-key", "")
	models := p.Models()
	for _, m := range models {
		if m.OwnedBy != "gemini" {
			t.Errorf("ModelInfo.OwnedBy = %q, want gemini", m.OwnedBy)
		}
	}
}

func TestGeminiProvider_CompleteStream_Interface(_ *testing.T) {
	p, _ := New("test-key", "")
	var _ core.StreamProvider = p
}

func TestGeminiProvider_Embed_Interface(_ *testing.T) {
	p, _ := New("test-key", "")
	var _ core.EmbeddingProvider = p
}

func TestGeminiProvider_Embed_BatchSuccess(t *testing.T) {
	dimensions := 64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/v1beta/models/gemini-embedding-001:batchEmbedContents" {
			t.Errorf("request path = %q, want /v1beta/models/gemini-embedding-001:batchEmbedContents", r.URL.Path)
		}
		if got := r.URL.Query().Get("key"); got != "test-key" {
			t.Errorf("key query = %q, want test-key", got)
		}
		var body struct {
			Requests []struct {
				Model   string `json:"model"`
				Content struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
				OutputDimensionality *int `json:"outputDimensionality"`
			} `json:"requests"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if len(body.Requests) != 2 {
			t.Fatalf("requests len = %d, want 2", len(body.Requests))
		}
		if body.Requests[0].Model != "models/gemini-embedding-001" || body.Requests[0].Content.Parts[0].Text != "first" {
			t.Errorf("first request = %+v", body.Requests[0])
		}
		if body.Requests[1].Content.Parts[0].Text != "second" {
			t.Errorf("second text = %q, want second", body.Requests[1].Content.Parts[0].Text)
		}
		if body.Requests[0].OutputDimensionality == nil || *body.Requests[0].OutputDimensionality != dimensions {
			t.Errorf("outputDimensionality = %v, want %d", body.Requests[0].OutputDimensionality, dimensions)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"embeddings":[{"values":[0.1,0.2]},{"values":[0.3,0.4]}],"usageMetadata":{"promptTokenCount":7,"totalTokenCount":7}}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	resp, err := p.Embed(context.Background(), core.EmbeddingRequest{
		Model:      "gemini-embedding-001",
		Input:      []string{"first", "second"},
		Dimensions: &dimensions,
	})
	if err != nil {
		t.Fatalf("Embed() error: %v", err)
	}
	if resp.Object != "list" || resp.Model != "gemini-embedding-001" {
		t.Errorf("response metadata = (%q, %q)", resp.Object, resp.Model)
	}
	if len(resp.Data) != 2 || resp.Data[0].Index != 0 || resp.Data[1].Index != 1 {
		t.Fatalf("response data = %+v", resp.Data)
	}
	if resp.Data[0].Embedding[0] != 0.1 || resp.Data[1].Embedding[1] != 0.4 {
		t.Errorf("embeddings = %+v", resp.Data)
	}
	if resp.Usage.PromptTokens != 7 || resp.Usage.TotalTokens != 7 {
		t.Errorf("usage = %+v, want 7 prompt/total", resp.Usage)
	}
}

func TestGeminiProvider_Embed_StringInput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Requests []struct {
				Content struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
			} `json:"requests"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if len(body.Requests) != 1 || body.Requests[0].Content.Parts[0].Text != "hello" {
			t.Fatalf("request body = %+v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"embeddings":[{"values":[1,2,3]}]}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	resp, err := p.Embed(context.Background(), core.EmbeddingRequest{
		Model: "text-embedding-004",
		Input: "hello",
	})
	if err != nil {
		t.Fatalf("Embed() error: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].Embedding[2] != 3 {
		t.Errorf("response data = %+v", resp.Data)
	}
}

func TestGeminiProvider_Embed_InvalidInput(t *testing.T) {
	p, _ := New("test-key", "")
	_, err := p.Embed(context.Background(), core.EmbeddingRequest{
		Model: "gemini-embedding-001",
		Input: []interface{}{"ok", 123},
	})
	if err == nil || !strings.Contains(err.Error(), "Input[1]") {
		t.Fatalf("Embed() error = %v, want invalid input error", err)
	}
}

func TestGeminiProvider_Embed_UpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad embedding request"}}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	_, err := p.Embed(context.Background(), core.EmbeddingRequest{
		Model: "gemini-embedding-001",
		Input: "hello",
	})
	if err == nil || !strings.Contains(err.Error(), "bad embedding request") {
		t.Fatalf("Embed() error = %v, want upstream error", err)
	}
}

func TestGeminiProvider_CompleteStream_MockSSE(t *testing.T) {
	sseData := "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Hello\"}],\"role\":\"model\"},\"finishReason\":\"\"}]}\n\n" +
		"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\" there\"}],\"role\":\"model\"},\"finishReason\":\"\"}]}\n\n" +
		"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"!\"}],\"role\":\"model\"},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":5,\"candidatesTokenCount\":3,\"totalTokenCount\":8}}\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseData))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	ch, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "gemini-2.0-flash",
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
	if chunks[0].Choices[0].Delta.Content != "Hello" {
		t.Errorf("delta content = %q, want Hello", chunks[0].Choices[0].Delta.Content)
	}
	if chunks[1].Choices[0].Delta.Content != " there" {
		t.Errorf("delta content = %q, want ' there'", chunks[1].Choices[0].Delta.Content)
	}
}
