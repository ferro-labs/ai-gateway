package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewGemini(t *testing.T) {
	p, err := NewGemini("test-key", "")
	if err != nil {
		t.Fatalf("NewGemini() error: %v", err)
	}
	if p.Name() != "gemini" {
		t.Errorf("Name() = %q, want gemini", p.Name())
	}
}

func TestGeminiProvider_SupportedModels(t *testing.T) {
	p, _ := NewGemini("test-key", "")
	models := p.SupportedModels()
	if len(models) == 0 {
		t.Error("SupportedModels() returned empty")
	}
	found := false
	for _, m := range models {
		if m == "gemini-2.0-flash" {
			found = true
		}
	}
	if !found {
		t.Error("gemini-2.0-flash not found")
	}
}

func TestGeminiProvider_SupportsModel(t *testing.T) {
	p, _ := NewGemini("test-key", "")
	if !p.SupportsModel("gemini-2.0-flash") {
		t.Error("expected gemini-2.0-flash to be supported")
	}
	if p.SupportsModel("gpt-4o") {
		t.Error("gemini should not support gpt-4o")
	}
}

func TestGeminiProvider_Models(t *testing.T) {
	p, _ := NewGemini("test-key", "")
	models := p.Models()
	for _, m := range models {
		if m.OwnedBy != "gemini" {
			t.Errorf("ModelInfo.OwnedBy = %q, want gemini", m.OwnedBy)
		}
	}
}

func TestGeminiProvider_CompleteStream_Interface(_ *testing.T) {
	p, _ := NewGemini("test-key", "")
	var _ StreamProvider = p
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

	p, _ := NewGemini("test-key", srv.URL)
	ch, err := p.CompleteStream(context.Background(), Request{
		Model:    "gemini-2.0-flash",
		Messages: []Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("CompleteStream() error: %v", err)
	}

	var chunks []StreamChunk
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
