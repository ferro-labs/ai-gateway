package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestNewDeepSeek(t *testing.T) {
	p, err := NewDeepSeek("test-key", "")
	if err != nil {
		t.Fatalf("NewDeepSeek() error: %v", err)
	}
	if p.Name() != "deepseek" {
		t.Errorf("Name() = %q, want deepseek", p.Name())
	}
}

func TestDeepSeekProvider_SupportedModels(t *testing.T) {
	p, _ := NewDeepSeek("test-key", "")
	models := p.SupportedModels()
	if len(models) == 0 {
		t.Error("SupportedModels() returned empty")
	}
	found := false
	for _, m := range models {
		if m == "deepseek-chat" {
			found = true
		}
	}
	if !found {
		t.Error("deepseek-chat not found")
	}
}

func TestDeepSeekProvider_SupportsModel(t *testing.T) {
	p, _ := NewDeepSeek("test-key", "")
	if !p.SupportsModel("deepseek-chat") {
		t.Error("expected deepseek-chat to be supported")
	}
	if !p.SupportsModel("deepseek-reasoner") {
		t.Error("expected deepseek-reasoner to be supported")
	}
	if p.SupportsModel("gpt-4o") {
		t.Error("deepseek should not support gpt-4o")
	}
}

func TestDeepSeekProvider_Models(t *testing.T) {
	p, _ := NewDeepSeek("test-key", "")
	models := p.Models()
	for _, m := range models {
		if m.OwnedBy != "deepseek" {
			t.Errorf("ModelInfo.OwnedBy = %q, want deepseek", m.OwnedBy)
		}
	}
}

func TestDeepSeekProvider_CompleteStream_Interface(_ *testing.T) {
	p, _ := NewDeepSeek("test-key", "")
	var _ StreamProvider = p
}

func TestDeepSeekProvider_CompleteStream_MockSSE(t *testing.T) {
	sseData := "data: {\"id\":\"chatcmpl-1\",\"model\":\"deepseek-chat\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"deepseek-chat\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"deepseek-chat\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" there\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"deepseek-chat\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseData))
	}))
	defer srv.Close()

	p, _ := NewDeepSeek("test-key", srv.URL)
	ch, err := p.CompleteStream(context.Background(), Request{
		Model:    "deepseek-chat",
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

func TestDeepSeekProvider_Complete_Integration(t *testing.T) {
	apiKey := os.Getenv("DEEPSEEK_API_KEY")
	if apiKey == "" {
		t.Skip("Skipping: DEEPSEEK_API_KEY not set")
	}

	p, _ := NewDeepSeek(apiKey, "")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := p.Complete(ctx, Request{
		Model:     "deepseek-chat",
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
