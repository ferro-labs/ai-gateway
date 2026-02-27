package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// TestNewAnthropic tests the Anthropic provider constructor.
func TestNewAnthropic(t *testing.T) {
	provider, err := NewAnthropic("sk-test-key", "")
	if err != nil {
		t.Fatalf("NewAnthropic() returned error: %v", err)
	}
	if provider == nil {
		t.Fatal("NewAnthropic() returned nil provider")
	}
	if provider.Name() != "anthropic" {
		t.Errorf("NewAnthropic() provider name = %v, want anthropic", provider.Name())
	}
}

// TestAnthropicProvider_SupportedModels tests the SupportedModels method.
func TestAnthropicProvider_SupportedModels(t *testing.T) {
	provider, _ := NewAnthropic("sk-test-key", "")

	models := provider.SupportedModels()
	if len(models) == 0 {
		t.Error("SupportedModels() returned empty list")
	}

	expected := []string{
		"claude-sonnet-4-20250514",
		"claude-3-5-sonnet-20241022",
		"claude-3-haiku-20240307",
		"claude-3-opus-20240229",
	}

	if len(models) != len(expected) {
		t.Fatalf("SupportedModels() returned %d models, want %d", len(models), len(expected))
	}

	for i, model := range models {
		if model != expected[i] {
			t.Errorf("SupportedModels()[%d] = %v, want %v", i, model, expected[i])
		}
	}
}

// TestAnthropicProvider_SupportsModel tests the SupportsModel method.
func TestAnthropicProvider_SupportsModel(t *testing.T) {
	provider, _ := NewAnthropic("sk-test-key", "")

	tests := []struct {
		name  string
		model string
		want  bool
	}{
		{"claude-sonnet-4 supported", "claude-sonnet-4-20250514", true},
		{"claude-3-5-sonnet supported", "claude-3-5-sonnet-20241022", true},
		{"claude-3-haiku supported", "claude-3-haiku-20240307", true},
		{"claude-3-opus supported", "claude-3-opus-20240229", true},
		{"unknown model passthrough", "claude-99", true},
		{"openai model rejected", "gpt-4o", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := provider.SupportsModel(tt.model); got != tt.want {
				t.Errorf("SupportsModel(%v) = %v, want %v", tt.model, got, tt.want)
			}
		})
	}
}

// TestAnthropicProvider_Models tests the Models method.
func TestAnthropicProvider_Models(t *testing.T) {
	provider, _ := NewAnthropic("sk-test-key", "")

	models := provider.Models()
	if len(models) == 0 {
		t.Error("Models() returned empty list")
	}
	for _, m := range models {
		if m.OwnedBy != "anthropic" {
			t.Errorf("ModelInfo.OwnedBy = %v, want anthropic", m.OwnedBy)
		}
	}
}

func TestAnthropicProvider_CompleteStream_Interface(_ *testing.T) {
	p, _ := NewAnthropic("sk-test-key", "")
	var _ StreamProvider = p
}

func TestAnthropicProvider_CompleteStream_MockSSE(t *testing.T) {
	sseData := `event: message_start
data: {"type":"message_start","message":{"id":"msg_123","model":"claude-3-haiku-20240307","role":"assistant"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}

event: message_stop
data: {"type":"message_stop"}

`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseData))
	}))
	defer srv.Close()

	p, _ := NewAnthropic("sk-test-key", srv.URL)
	ctx := context.Background()
	ch, err := p.CompleteStream(ctx, Request{
		Model:    "claude-3-haiku-20240307",
		Messages: []Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("CompleteStream() error: %v", err)
	}

	var chunks []StreamChunk
	for c := range ch {
		chunks = append(chunks, c)
	}

	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}
	if chunks[0].ID != "msg_123" {
		t.Errorf("chunk ID = %q, want msg_123", chunks[0].ID)
	}
	//nolint:goconst // "Hello" appears in multiple test strings; fine in tests
	if chunks[0].Choices[0].Delta.Content != "Hello" {
		t.Errorf("first delta content = %q, want Hello", chunks[0].Choices[0].Delta.Content)
	}
	if chunks[1].Choices[0].Delta.Content != " world" {
		t.Errorf("second delta content = %q, want ' world'", chunks[1].Choices[0].Delta.Content)
	}
}

// TestAnthropicProvider_Complete_Integration tests actual API calls.
// This test only runs if ANTHROPIC_API_KEY environment variable is set.
func TestAnthropicProvider_Complete_Integration(t *testing.T) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("Skipping integration test: ANTHROPIC_API_KEY not set")
	}

	provider, err := NewAnthropic(apiKey, "")
	if err != nil {
		t.Fatalf("NewAnthropic() returned error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req := Request{
		Model: "claude-3-haiku-20240307",
		Messages: []Message{
			{Role: "system", Content: "You are a helpful assistant."},
			{Role: "user", Content: "Say 'test successful' and nothing else."},
		},
		Temperature: floatPtr(0.0),
		MaxTokens:   intPtr(10),
	}

	resp, err := provider.Complete(ctx, req)
	if err != nil {
		t.Fatalf("Complete() failed: %v", err)
	}

	if resp.ID == "" {
		t.Error("Response ID is empty")
	}
	if resp.Model == "" {
		t.Error("Response Model is empty")
	}
	if len(resp.Choices) == 0 {
		t.Error("Response has no choices")
	}
	if resp.Choices[0].Message.Content == "" {
		t.Error("Response message content is empty")
	}

	t.Logf("Response: %+v", resp)
}
