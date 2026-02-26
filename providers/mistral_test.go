package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewMistral(t *testing.T) {
	p, err := NewMistral("test-key", "")
	if err != nil {
		t.Fatalf("NewMistral() error: %v", err)
	}
	if p.Name() != "mistral" {
		t.Errorf("Name() = %q, want mistral", p.Name())
	}
}

func TestMistralProvider_SupportedModels(t *testing.T) {
	p, _ := NewMistral("test-key", "")
	models := p.SupportedModels()
	if len(models) == 0 {
		t.Error("SupportedModels() returned empty")
	}
	found := false
	for _, m := range models {
		if m == "mistral-large-latest" {
			found = true
		}
	}
	if !found {
		t.Error("mistral-large-latest not found")
	}
}

func TestMistralProvider_SupportsModel(t *testing.T) {
	p, _ := NewMistral("test-key", "")
	if !p.SupportsModel("mistral-large-latest") {
		t.Error("expected mistral-large-latest to be supported")
	}
	if p.SupportsModel("gpt-4o") {
		t.Error("mistral should not support gpt-4o")
	}
}

func TestMistralProvider_Models(t *testing.T) {
	p, _ := NewMistral("test-key", "")
	models := p.Models()
	for _, m := range models {
		if m.OwnedBy != "mistral" {
			t.Errorf("ModelInfo.OwnedBy = %q, want mistral", m.OwnedBy)
		}
	}
}

func TestMistralProvider_CompleteStream_Interface(_ *testing.T) {
	p, _ := NewMistral("test-key", "")
	var _ StreamProvider = p
}

func TestMistralProvider_CompleteStream_MockSSE(t *testing.T) {
	sseData := "data: {\"id\":\"chatcmpl-1\",\"model\":\"mistral-large-latest\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"mistral-large-latest\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"mistral-large-latest\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" there\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"mistral-large-latest\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseData))
	}))
	defer srv.Close()

	p, _ := NewMistral("test-key", srv.URL)
	ch, err := p.CompleteStream(context.Background(), Request{
		Model:    "mistral-large-latest",
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
	if chunks[1].Choices[0].Delta.Content != "Hello" {
		t.Errorf("delta content = %q, want Hello", chunks[1].Choices[0].Delta.Content)
	}
	if chunks[2].Choices[0].Delta.Content != " there" {
		t.Errorf("delta content = %q, want ' there'", chunks[2].Choices[0].Delta.Content)
	}
}
