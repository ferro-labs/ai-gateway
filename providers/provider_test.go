package providers

import (
	"testing"
)

func TestRequest_Validate(t *testing.T) {
	tests := []struct {
		name    string
		req     Request
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid request",
			req: Request{
				Model: "gpt-4o",
				Messages: []Message{
					{Role: "user", Content: "Hello"},
				},
			},
			wantErr: false,
		},
		{
			name: "missing model",
			req: Request{
				Messages: []Message{
					{Role: "user", Content: "Hello"},
				},
			},
			wantErr: true,
			errMsg:  "model is required",
		},
		{
			name: "missing messages",
			req: Request{
				Model:    "gpt-4o",
				Messages: []Message{},
			},
			wantErr: true,
			errMsg:  "at least one message is required",
		},
		{
			name: "invalid temperature - too low",
			req: Request{
				Model: "gpt-4o",
				Messages: []Message{
					{Role: "user", Content: "Hello"},
				},
				Temperature: floatPtr(-0.1),
			},
			wantErr: true,
			errMsg:  "temperature must be between 0 and 2",
		},
		{
			name: "invalid temperature - too high",
			req: Request{
				Model: "gpt-4o",
				Messages: []Message{
					{Role: "user", Content: "Hello"},
				},
				Temperature: floatPtr(2.1),
			},
			wantErr: true,
			errMsg:  "temperature must be between 0 and 2",
		},
		{
			name: "valid temperature",
			req: Request{
				Model: "gpt-4o",
				Messages: []Message{
					{Role: "user", Content: "Hello"},
				},
				Temperature: floatPtr(0.7),
			},
			wantErr: false,
		},
		{
			name: "invalid max tokens",
			req: Request{
				Model: "gpt-4o",
				Messages: []Message{
					{Role: "user", Content: "Hello"},
				},
				MaxTokens: intPtr(0),
			},
			wantErr: true,
			errMsg:  "max_tokens must be positive",
		},
		{
			name: "valid max tokens",
			req: Request{
				Model: "gpt-4o",
				Messages: []Message{
					{Role: "user", Content: "Hello"},
				},
				MaxTokens: intPtr(100),
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.req.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Request.Validate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil && tt.errMsg != "" && err.Error() != tt.errMsg {
				t.Errorf("Request.Validate() error message = %v, want %v", err.Error(), tt.errMsg)
			}
		})
	}
}

func TestMessage(t *testing.T) {
	tests := []struct {
		name string
		msg  Message
	}{
		{
			name: "user message",
			msg: Message{
				Role:    "user",
				Content: "Hello, world!",
			},
		},
		{
			name: "system message",
			msg: Message{
				Role:    "system",
				Content: "You are a helpful assistant.",
			},
		},
		{
			name: "assistant message",
			msg: Message{
				Role:    "assistant",
				Content: "How can I help you?",
			},
		},
		{
			name: "message with name",
			msg: Message{
				Role:    "user",
				Content: "Question",
				Name:    "Alice",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.msg.Role == "" {
				t.Error("Message role should not be empty")
			}
			if tt.msg.Content == "" {
				t.Error("Message content should not be empty")
			}
		})
	}
}

func TestUsage(t *testing.T) {
	usage := Usage{
		PromptTokens:     100,
		CompletionTokens: 50,
		TotalTokens:      150,
	}

	if usage.TotalTokens != usage.PromptTokens+usage.CompletionTokens {
		t.Error("Total tokens should equal prompt + completion tokens")
	}
}

func TestModelInfo(t *testing.T) {
	model := ModelInfo{
		ID:      "gpt-4o",
		Object:  "model",
		OwnedBy: "openai",
	}

	if model.ID == "" {
		t.Error("ModelInfo ID should not be empty")
	}

	if model.Object != "model" {
		t.Errorf("ModelInfo Object = %q, want \"model\"", model.Object)
	}

	if model.OwnedBy == "" {
		t.Error("ModelInfo OwnedBy should not be empty")
	}
}

func TestChoice(t *testing.T) {
	choice := Choice{
		Index: 0,
		Message: Message{
			Role:    "assistant",
			Content: "Hello!",
		},
		FinishReason: "stop",
	}

	if choice.Index < 0 {
		t.Error("Choice index should not be negative")
	}

	if choice.Message.Role != "assistant" {
		t.Error("Choice message should have assistant role")
	}

	validFinishReasons := map[string]bool{
		"stop":           true,
		"length":         true,
		"tool_calls":     true,
		"content_filter": true,
	}

	if !validFinishReasons[choice.FinishReason] {
		t.Errorf("Choice has invalid finish reason: %s", choice.FinishReason)
	}
}

func TestResponse(t *testing.T) {
	resp := Response{
		ID:       "test-123",
		Model:    "gpt-4o",
		Provider: "openai",
		Choices: []Choice{
			{
				Index: 0,
				Message: Message{
					Role:    "assistant",
					Content: "Hello!",
				},
				FinishReason: "stop",
			},
		},
		Usage: Usage{
			PromptTokens:     10,
			CompletionTokens: 5,
			TotalTokens:      15,
		},
	}

	if resp.ID == "" {
		t.Error("Response ID should not be empty")
	}

	if resp.Model == "" {
		t.Error("Response Model should not be empty")
	}

	if resp.Provider == "" {
		t.Error("Response Provider should not be empty")
	}

	if len(resp.Choices) == 0 {
		t.Error("Response should have at least one choice")
	}

	if resp.Usage.TotalTokens != resp.Usage.PromptTokens+resp.Usage.CompletionTokens {
		t.Error("Response usage total should match sum")
	}
}

// Helper functions for creating pointers (used by tests in this package)
func floatPtr(f float64) *float64 {
	return &f
}

func intPtr(i int) *int {
	return &i
}
