package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

const (
	ai21TestContentHello = "Hello"
	ai21TestContentWorld = " world"
	ai21TestChunkID      = "chatcmpl-1"
)

func TestNewAI21(t *testing.T) {
	p, err := NewAI21("test-key", "")
	if err != nil {
		t.Fatalf("NewAI21() error: %v", err)
	}
	if p.Name() != "ai21" {
		t.Errorf("Name() = %q, want ai21", p.Name())
	}
}

func TestAI21Provider_SupportedModels(t *testing.T) {
	p, _ := NewAI21("test-key", "")
	models := p.SupportedModels()
	if len(models) == 0 {
		t.Error("SupportedModels() returned empty")
	}
	found := false
	for _, m := range models {
		if m == "jamba-1.5-large" {
			found = true
		}
	}
	if !found {
		t.Error("jamba-1.5-large not found in supported models")
	}
}

func TestAI21Provider_SupportsModel(t *testing.T) {
	p, _ := NewAI21("test-key", "")
	if !p.SupportsModel("jamba-1.5-large") {
		t.Error("expected jamba-1.5-large to be supported")
	}
	if !p.SupportsModel("any-model") {
		t.Error("passthrough: expected all models to return true")
	}
}

func TestAI21Provider_Models(t *testing.T) {
	p, _ := NewAI21("test-key", "")
	models := p.Models()
	for _, m := range models {
		if m.OwnedBy != "ai21" {
			t.Errorf("ModelInfo.OwnedBy = %q, want ai21", m.OwnedBy)
		}
	}
}

func TestAI21Provider_isJambaModel(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{"jamba-1.5-large", true},
		{"jamba-1.5-mini", true},
		{"jamba-instruct", true},
		{"j2-ultra", false},
		{"j2-mid", false},
	}
	for _, tt := range tests {
		got := isJambaModel(tt.model)
		if got != tt.want {
			t.Errorf("isJambaModel(%q) = %v, want %v", tt.model, got, tt.want)
		}
	}
}

func TestAI21Provider_CompleteStream_Interface(_ *testing.T) {
	p, _ := NewAI21("test-key", "")
	var _ StreamProvider = p
}

func TestAI21Provider_CompleteStream_JambaModel(t *testing.T) {
	sseData := "data: {\"id\":\"chatcmpl-1\",\"model\":\"jamba-1.5-mini\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"jamba-1.5-mini\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"jamba-1.5-mini\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" world\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"jamba-1.5-mini\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseData))
	}))
	defer srv.Close()

	p, _ := NewAI21("test-key", srv.URL)
	ch, err := p.CompleteStream(context.Background(), Request{
		Model:    "jamba-1.5-mini",
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
	if chunks[1].Choices[0].Delta.Content != ai21TestContentHello {
		t.Errorf("delta content = %q, want Hello", chunks[1].Choices[0].Delta.Content)
	}
	if chunks[2].Choices[0].Delta.Content != ai21TestContentWorld {
		t.Errorf("delta content = %q, want ' world'", chunks[2].Choices[0].Delta.Content)
	}
}

func TestAI21Provider_CompleteStream_NonJambaReturnsError(t *testing.T) {
	p, _ := NewAI21("test-key", "")
	_, err := p.CompleteStream(context.Background(), Request{
		Model:    "j2-ultra",
		Messages: []Message{{Role: "user", Content: "Hi"}},
	})
	if err == nil {
		t.Fatal("expected error for non-Jamba model, got nil")
	}
}

func TestAI21Provider_Complete_JambaModel(t *testing.T) {
	respBody := `{"id":"chatcmpl-1","model":"jamba-1.5-mini","choices":[{"index":0,"message":{"role":"assistant","content":"Hello!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(respBody))
	}))
	defer srv.Close()

	p, _ := NewAI21("test-key", srv.URL)
	resp, err := p.Complete(context.Background(), Request{
		Model:    "jamba-1.5-mini",
		Messages: []Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if resp.ID != ai21TestChunkID {
		t.Errorf("Response.ID = %q, want chatcmpl-1", resp.ID)
	}
}

func TestAI21Provider_Complete_JurassicModel(t *testing.T) {
	respBody := `{"id":"j2-1","completions":[{"data":{"text":"Hello from Jurassic!","tokens":[]},"finishReason":{"reason":"length"}}]}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(respBody))
	}))
	defer srv.Close()

	p, _ := NewAI21("test-key", srv.URL)
	resp, err := p.Complete(context.Background(), Request{
		Model:    "j2-ultra",
		Messages: []Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if len(resp.Choices) == 0 {
		t.Fatal("expected at least one choice")
	}
	if resp.Choices[0].Message.Content != "Hello from Jurassic!" {
		t.Errorf("choice content = %q, want 'Hello from Jurassic!'", resp.Choices[0].Message.Content)
	}
}
