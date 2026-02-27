package providers

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestNewOpenAI tests the OpenAI provider constructor.
func TestNewOpenAI(t *testing.T) {
	provider, err := NewOpenAI("sk-test-key", "")
	if err != nil {
		t.Fatalf("NewOpenAI() returned error: %v", err)
	}
	if provider == nil {
		t.Fatal("NewOpenAI() returned nil provider")
	}
	if provider.Name() != "openai" {
		t.Errorf("NewOpenAI() provider name = %v, want openai", provider.Name())
	}
}

// TestOpenAIProvider_SupportedModels tests the SupportedModels method.
func TestOpenAIProvider_SupportedModels(t *testing.T) {
	provider, _ := NewOpenAI("sk-test-key", "")

	models := provider.SupportedModels()
	if len(models) == 0 {
		t.Error("SupportedModels() returned empty list")
	}

	// Check that gpt-4o is in the list
	found := false
	for _, model := range models {
		if model == "gpt-4o" {
			found = true
			break
		}
	}
	if !found {
		t.Error("gpt-4o not found in models list")
	}
}

// TestOpenAIProvider_SupportsModel tests the SupportsModel method.
// With passthrough enabled, all model strings are accepted.
func TestOpenAIProvider_SupportsModel(t *testing.T) {
	provider, _ := NewOpenAI("sk-test-key", "")

	tests := []struct {
		name  string
		model string
		want  bool
	}{
		{"gpt-4o supported", "gpt-4o", true},
		{"gpt-4-turbo supported", "gpt-4-turbo", true},
		{"gpt-3.5-turbo supported", "gpt-3.5-turbo", true},
		{"unknown model passthrough", "gpt-99", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := provider.SupportsModel(tt.model); got != tt.want {
				t.Errorf("SupportsModel(%v) = %v, want %v", tt.model, got, tt.want)
			}
		})
	}
}

// TestOpenAIProvider_Models tests the Models method.
func TestOpenAIProvider_Models(t *testing.T) {
	provider, _ := NewOpenAI("sk-test-key", "")

	models := provider.Models()
	if len(models) == 0 {
		t.Error("Models() returned empty list")
	}
	for _, m := range models {
		if m.OwnedBy != "openai" {
			t.Errorf("ModelInfo.OwnedBy = %v, want openai", m.OwnedBy)
		}
	}
}

// TestOpenAIProvider_Complete_Integration tests actual API calls.
// This test only runs if OPENAI_API_KEY environment variable is set.
func TestOpenAIProvider_Complete_Integration(t *testing.T) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("Skipping integration test: OPENAI_API_KEY not set")
	}

	provider, err := NewOpenAI(apiKey, "")
	if err != nil {
		t.Fatalf("NewOpenAI() returned error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req := Request{
		Model: "gpt-3.5-turbo",
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
