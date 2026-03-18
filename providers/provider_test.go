package providers

import (
	"encoding/json"
	"fmt"
	"strings"
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

func TestMessage_UnmarshalJSONStringContent(t *testing.T) {
	var msg Message
	err := json.Unmarshal([]byte(`{"role":"user","content":"hello","name":"alice"}`), &msg)
	if err != nil {
		t.Fatalf("unmarshal message: %v", err)
	}
	if msg.Role != "user" {
		t.Fatalf("role = %q, want user", msg.Role)
	}
	if msg.Content != "hello" {
		t.Fatalf("content = %q, want hello", msg.Content)
	}
	if msg.Name != "alice" {
		t.Fatalf("name = %q, want alice", msg.Name)
	}
}

func TestMessage_UnmarshalMultipartContent(t *testing.T) {
	var msg Message
	err := json.Unmarshal([]byte(`{
		"role":"user",
		"content":[
			{"type":"text","text":"hello "},
			{"type":"image_url","image_url":{"url":"https://example.com/image.png"}},
			{"type":"text","text":"world"}
		]
	}`), &msg)
	if err != nil {
		t.Fatalf("unmarshal message: %v", err)
	}
	if got := len(msg.ContentParts); got != 3 {
		t.Fatalf("content parts = %d, want 3", got)
	}
	if msg.Content != "hello world" {
		t.Fatalf("content = %q, want %q", msg.Content, "hello world")
	}
}

func TestMessage_UnmarshalIgnoresUnknownFields(t *testing.T) {
	var msg Message
	err := json.Unmarshal([]byte(`{
		"role":"assistant",
		"content":"ok",
		"metadata":{"trace_id":"abc","nested":{"a":[1,2,3]}}
	}`), &msg)
	if err != nil {
		t.Fatalf("unmarshal message: %v", err)
	}
	if msg.Role != "assistant" || msg.Content != "ok" {
		t.Fatalf("unexpected message: %+v", msg)
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

func TestParseStatusCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"nil error", nil, 0},
		{"standard 429", fmt.Errorf("provider error (429): rate limited"), 429},
		{"standard 503", fmt.Errorf("cohere API error (503): service unavailable"), 503},
		{"standard 400", fmt.Errorf("mistral API error (400): bad request"), 400},
		{"no status code", fmt.Errorf("network timeout"), 0},
		{"partial number", fmt.Errorf("attempt 3 failed"), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseStatusCode(tt.err)
			if got != tt.want {
				t.Errorf("ParseStatusCode(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}

func BenchmarkMessageUnmarshalStringContent(b *testing.B) {
	payload := []byte(`{
		"role":"user",
		"content":"Summarize the latest deployment status.",
		"name":"alice",
		"metadata":{"tenant":"bench","request_id":"req-123"}
	}`)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var msg Message
		if err := json.Unmarshal(payload, &msg); err != nil {
			b.Fatal(err)
		}
	}
}

func TestStreamChunkUsageOmitEmpty(t *testing.T) {
	chunk := StreamChunk{
		ID:      "test-id",
		Object:  "chat.completion.chunk",
		Created: 1,
		Model:   "gpt-4o",
		Choices: []StreamChoice{
			{
				Index: 0,
				Delta: MessageDelta{Content: "hi"},
			},
		},
	}

	data, err := json.Marshal(chunk)
	if err != nil {
		t.Fatalf("Marshal(StreamChunk) error = %v", err)
	}
	if strings.Contains(string(data), `"usage"`) {
		t.Fatalf("usage should be omitted when nil, got: %s", string(data))
	}

	chunk.Usage = &Usage{
		PromptTokens:     1,
		CompletionTokens: 1,
		TotalTokens:      2,
	}
	data, err = json.Marshal(chunk)
	if err != nil {
		t.Fatalf("Marshal(StreamChunk with usage) error = %v", err)
	}
	if !strings.Contains(string(data), `"usage"`) {
		t.Fatalf("usage should be present when set, got: %s", string(data))
	}
}
