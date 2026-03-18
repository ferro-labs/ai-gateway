package together

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

func TestNewTogether(t *testing.T) {
	p, err := New("test-key", "")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if p.Name() != "together" {
		t.Errorf("Name() = %q, want together", p.Name())
	}
}

func TestTogetherProvider_SupportedModels(t *testing.T) {
	p, _ := New("test-key", "")
	models := p.SupportedModels()
	if len(models) == 0 {
		t.Error("SupportedModels() returned empty")
	}
	found := false
	for _, m := range models {
		if m == "meta-llama/Meta-Llama-3.1-8B-Instruct-Turbo" {
			found = true
		}
	}
	if !found {
		t.Error("meta-llama/Meta-Llama-3.1-8B-Instruct-Turbo not found")
	}
}

func TestTogetherProvider_SupportsModel(t *testing.T) {
	p, _ := New("test-key", "")
	if !p.SupportsModel("meta-llama/Meta-Llama-3.1-8B-Instruct-Turbo") {
		t.Error("expected meta-llama/Meta-Llama-3.1-8B-Instruct-Turbo to be supported")
	}
	if !p.SupportsModel("gpt-4o") {
		t.Error("passthrough: expected any model including gpt-4o to return true")
	}
}

func TestTogetherProvider_Models(t *testing.T) {
	p, _ := New("test-key", "")
	models := p.Models()
	for _, m := range models {
		if m.OwnedBy != "together" {
			t.Errorf("ModelInfo.OwnedBy = %q, want together", m.OwnedBy)
		}
	}
}

func TestTogetherProvider_CompleteStream_Interface(_ *testing.T) {
	p, _ := New("test-key", "")
	var _ core.StreamProvider = p
}

func TestTogetherProvider_CompleteStream_MockSSE(t *testing.T) {
	sseData := "data: {\"id\":\"cmpl-1\",\"model\":\"meta-llama/Meta-Llama-3.1-8B-Instruct-Turbo\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"cmpl-1\",\"model\":\"meta-llama/Meta-Llama-3.1-8B-Instruct-Turbo\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hi\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"cmpl-1\",\"model\":\"meta-llama/Meta-Llama-3.1-8B-Instruct-Turbo\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"!\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"cmpl-1\",\"model\":\"meta-llama/Meta-Llama-3.1-8B-Instruct-Turbo\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseData))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	ch, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "meta-llama/Meta-Llama-3.1-8B-Instruct-Turbo",
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
	if chunks[1].Choices[0].Delta.Content != "Hi" {
		t.Errorf("delta content = %q, want Hi", chunks[1].Choices[0].Delta.Content)
	}
	if chunks[2].Choices[0].Delta.Content != "!" {
		t.Errorf("delta content = %q, want !", chunks[2].Choices[0].Delta.Content)
	}
}

func TestTogetherProvider_Complete_Integration(t *testing.T) {
	apiKey := os.Getenv("TOGETHER_API_KEY")
	if apiKey == "" {
		t.Skip("Skipping: TOGETHER_API_KEY not set")
	}

	p, _ := New(apiKey, "")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := p.Complete(ctx, core.Request{
		Model:     "meta-llama/Meta-Llama-3.1-8B-Instruct-Turbo",
		Messages:  []core.Message{{Role: "user", Content: "Say 'test ok' and nothing else."}},
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

func intPtr(i int) *int { return &i }
