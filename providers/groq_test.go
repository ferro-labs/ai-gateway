package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestNewGroq(t *testing.T) {
	p, err := NewGroq("test-key", "")
	if err != nil {
		t.Fatalf("NewGroq() error: %v", err)
	}
	if p.Name() != "groq" {
		t.Errorf("Name() = %q, want groq", p.Name())
	}
}

func TestGroqProvider_SupportedModels(t *testing.T) {
	p, _ := NewGroq("test-key", "")
	models := p.SupportedModels()
	if len(models) == 0 {
		t.Error("SupportedModels() returned empty")
	}
	found := false
	for _, m := range models {
		if m == "llama-3.3-70b-versatile" {
			found = true
		}
	}
	if !found {
		t.Error("llama-3.3-70b-versatile not found")
	}
}

func TestGroqProvider_SupportsModel(t *testing.T) {
	p, _ := NewGroq("test-key", "")
	if !p.SupportsModel("llama-3.3-70b-versatile") {
		t.Error("expected llama-3.3-70b-versatile to be supported")
	}
	if !p.SupportsModel("gpt-4o") {
		t.Error("passthrough: expected any model to return true")
	}
}

func TestGroqProvider_Models(t *testing.T) {
	p, _ := NewGroq("test-key", "")
	models := p.Models()
	for _, m := range models {
		if m.OwnedBy != "groq" {
			t.Errorf("ModelInfo.OwnedBy = %q, want groq", m.OwnedBy)
		}
	}
}

func TestGroqProvider_CompleteStream_Interface(_ *testing.T) {
	p, _ := NewGroq("test-key", "")
	var _ StreamProvider = p
}

func TestGroqProvider_CompleteStream_MockSSE(t *testing.T) {
	sseData := "data: {\"id\":\"chatcmpl-1\",\"model\":\"llama-3.1-8b-instant\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"llama-3.1-8b-instant\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"llama-3.1-8b-instant\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" there\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"llama-3.1-8b-instant\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseData))
	}))
	defer srv.Close()

	p, _ := NewGroq("test-key", srv.URL+"/openai")
	ch, err := p.CompleteStream(context.Background(), Request{
		Model:    "llama-3.1-8b-instant",
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

func TestGroqProvider_Complete_Integration(t *testing.T) {
	apiKey := os.Getenv("GROQ_API_KEY")
	if apiKey == "" {
		t.Skip("Skipping: GROQ_API_KEY not set")
	}

	p, _ := NewGroq(apiKey, "")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := p.Complete(ctx, Request{
		Model:     "llama-3.1-8b-instant",
		Messages:  []Message{{Role: "user", Content: "Say 'test ok' and nothing else."}},
		MaxTokens: intPtr(10),
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if resp.ID == "" {
		t.Error("Response ID is empty")
	}
	t.Logf("Response: %+v", resp)
}
