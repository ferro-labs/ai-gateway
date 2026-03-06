package ollama

import (
	"context"
	core "github.com/ferro-labs/ai-gateway/providers/core"
	"net/http"
	"net/http/httptest"
	"testing"
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
