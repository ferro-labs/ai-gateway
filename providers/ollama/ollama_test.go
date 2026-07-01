package ollama

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

func TestNewOllama(t *testing.T) {
	p, err := New("", nil)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if p.Name() != "ollama" {
		t.Errorf("Name() = %q, want ollama", p.Name())
	}
}

func TestNewOllama_DefaultModels(t *testing.T) {
	p, _ := New("", nil)
	models := p.SupportedModels()
	if len(models) != 1 || models[0] != "llama3.2" {
		t.Errorf("default SupportedModels() = %v, want [llama3.2]", models)
	}
}

func TestNewOllama_CustomModels(t *testing.T) {
	p, _ := New("", []string{"llama3.2", "mistral", "phi3"})
	models := p.SupportedModels()
	if len(models) != 3 {
		t.Errorf("SupportedModels() returned %d models, want 3", len(models))
	}
}

func TestOllamaProvider_SupportsModel(t *testing.T) {
	p, _ := New("", []string{"llama3.2", "mistral"})
	if !p.SupportsModel("llama3.2") {
		t.Error("expected llama3.2 to be supported")
	}
	if !p.SupportsModel("mistral") {
		t.Error("expected mistral to be supported")
	}
	if !p.SupportsModel("gpt-4o") {
		t.Error("passthrough: expected any model to return true")
	}
}

func TestOllamaProvider_Models(t *testing.T) {
	p, _ := New("", []string{"llama3.2"})
	models := p.Models()
	for _, m := range models {
		if m.OwnedBy != "ollama" {
			t.Errorf("ModelInfo.OwnedBy = %q, want ollama", m.OwnedBy)
		}
	}
}

func TestOllamaProvider_CompleteStream_Interface(_ *testing.T) {
	p, _ := New("", nil)
	var _ core.StreamProvider = p
}

func TestOllamaProvider_CompleteStream_MockSSE(t *testing.T) {
	sseData := "data: {\"id\":\"chatcmpl-1\",\"model\":\"llama3.2\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"llama3.2\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"llama3.2\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" there\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"llama3.2\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseData))
	}))
	defer srv.Close()

	p, _ := New(srv.URL, []string{"llama3.2"})
	ch, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "llama3.2",
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

func TestOllamaProvider_Embed_Interface(_ *testing.T) {
	p, _ := New("", nil)
	var _ core.EmbeddingProvider = p
}

func TestOllamaProvider_Embed_MockHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/embed" {
			t.Errorf("path = %q, want /api/embed", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization = %q, want empty (no auth)", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["model"] != "nomic-embed-text" {
			t.Errorf("model = %v, want nomic-embed-text", body["model"])
		}
		arr, ok := body["input"].([]any)
		if !ok || len(arr) != 2 || arr[0] != "hello" || arr[1] != "world" {
			t.Errorf("input = %v, want [hello world]", body["input"])
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"model":"nomic-embed-text","embeddings":[[0.1,0.2],[0.3,0.4]],"prompt_eval_count":6}`))
	}))
	defer srv.Close()

	p, _ := New(srv.URL, []string{"nomic-embed-text"})
	resp, err := p.Embed(context.Background(), core.EmbeddingRequest{
		Model: "nomic-embed-text",
		Input: []string{"hello", "world"},
	})
	if err != nil {
		t.Fatalf("Embed() error: %v", err)
	}
	if resp.Object != "list" {
		t.Errorf("Object = %q, want list", resp.Object)
	}
	if resp.Model != "nomic-embed-text" {
		t.Errorf("Model = %q, want nomic-embed-text", resp.Model)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("Data length = %d, want 2", len(resp.Data))
	}
	if resp.Data[0].Object != "embedding" || resp.Data[0].Index != 0 || !reflect.DeepEqual(resp.Data[0].Embedding, []float64{0.1, 0.2}) {
		t.Errorf("Data[0] = %+v, want embedding at index 0", resp.Data[0])
	}
	if resp.Data[1].Index != 1 || !reflect.DeepEqual(resp.Data[1].Embedding, []float64{0.3, 0.4}) {
		t.Errorf("Data[1] = %+v, want embedding at index 1", resp.Data[1])
	}
	if resp.Usage.PromptTokens != 6 || resp.Usage.TotalTokens != 6 {
		t.Errorf("Usage = %+v, want prompt=6 total=6", resp.Usage)
	}
}

func TestOllamaProvider_DiscoverModels_Interface(_ *testing.T) {
	p, _ := New("", nil)
	var _ core.DiscoveryProvider = p
}

func TestOllamaProvider_DiscoverModels_ParsesTagsNoAuth(t *testing.T) {
	firstModified := "2025-02-03T04:05:06Z"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/api/tags" {
			t.Errorf("path = %q, want /api/tags", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization = %q, want empty (self-hosted Ollama is unauthenticated)", got)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"models": [
				{"name":"llama3.2","modified_at":"` + firstModified + `"},
				{"model":"mistral","modified_at":"2025-02-04T04:05:06.123Z"},
				{"name":"llama3.2","modified_at":"2025-02-05T04:05:06Z"}
			]
		}`))
	}))
	defer srv.Close()

	p, _ := New(srv.URL, []string{"configured-model"})
	models, err := p.DiscoverModels(context.Background())
	if err != nil {
		t.Fatalf("DiscoverModels() error: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("models length = %d, want 2 (duplicate dropped)", len(models))
	}
	if models[0].ID != "llama3.2" || models[0].Object != "model" || models[0].OwnedBy != Name {
		t.Errorf("first model = %#v, want llama3.2 model owned by %s", models[0], Name)
	}
	if models[0].Created != mustUnix(t, firstModified) {
		t.Errorf("first created = %d, want %d", models[0].Created, mustUnix(t, firstModified))
	}
	if models[1].ID != "mistral" {
		t.Errorf("second model ID = %q, want mistral", models[1].ID)
	}
}

func TestOllamaProvider_DiscoverModels_Non200Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
	}))
	defer srv.Close()

	p, _ := New(srv.URL, nil)
	if _, err := p.DiscoverModels(context.Background()); err == nil {
		t.Fatal("DiscoverModels() error = nil, want error on non-200")
	}
}

func mustUnix(t *testing.T, value string) int64 {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatalf("failed to parse time %q: %v", value, err)
	}
	return parsed.Unix()
}

func TestOllamaProvider_Embed_RejectsNonFloatEncoding(t *testing.T) {
	p, _ := New("http://127.0.0.1:0", []string{"nomic-embed-text"})
	_, err := p.Embed(context.Background(), core.EmbeddingRequest{
		Model:          "nomic-embed-text",
		Input:          "hi",
		EncodingFormat: "base64",
	})
	if err == nil {
		t.Fatal("Embed() error = nil, want unsupported encoding_format error")
	}
}
