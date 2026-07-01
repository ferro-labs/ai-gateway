package anthropic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// TestAnthropicProvider_DiscoverModels verifies live model discovery uses the
// x-api-key + anthropic-version headers (not Bearer) and maps the response.
func TestAnthropicProvider_DiscoverModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %q, want /v1/models", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "sk-test-key" {
			t.Errorf("x-api-key = %q, want sk-test-key", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("anthropic-version = %q, want 2023-06-01", r.Header.Get("anthropic-version"))
		}
		if r.Header.Get("Authorization") != "" {
			t.Errorf("Authorization = %q, want empty (anthropic uses x-api-key)", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"claude-sonnet-4-20250514","object":"model","created":1700000000,"owned_by":"anthropic"},{"id":"claude-3-haiku-20240307","object":"model"}]}`))
	}))
	defer srv.Close()

	p, _ := New("sk-test-key", srv.URL)
	models, err := p.DiscoverModels(context.Background())
	if err != nil {
		t.Fatalf("DiscoverModels() error: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	if models[0].ID != "claude-sonnet-4-20250514" || models[0].OwnedBy != "anthropic" {
		t.Errorf("unexpected model[0]: %+v", models[0])
	}
	if models[1].ID != "claude-3-haiku-20240307" || models[1].OwnedBy != "anthropic" {
		t.Errorf("model[1] owned_by fallback = %q, want anthropic", models[1].OwnedBy)
	}
}

// TestNewAnthropic tests the Anthropic provider constructor.
func TestNewAnthropic(t *testing.T) {
	provider, err := New("sk-test-key", "")
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}
	if provider == nil {
		t.Fatal("New() returned nil provider")
	}
	if provider.Name() != "anthropic" {
		t.Errorf("New() provider name = %v, want anthropic", provider.Name())
	}
}

// TestAnthropicProvider_SupportedModels tests the SupportedModels method.
func TestAnthropicProvider_SupportedModels(t *testing.T) {
	provider, _ := New("sk-test-key", "")

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
	provider, _ := New("sk-test-key", "")

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
	provider, _ := New("sk-test-key", "")

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
	p, _ := New("sk-test-key", "")
	var _ core.StreamProvider = p
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

	p, _ := New("sk-test-key", srv.URL)
	ctx := context.Background()
	ch, err := p.CompleteStream(ctx, core.Request{
		Model:    "claude-3-haiku-20240307",
		Messages: []core.Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("CompleteStream() error: %v", err)
	}

	var chunks []core.StreamChunk
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

func TestAnthropicProvider_CompleteStream_ForwardsToolUseDeltas(t *testing.T) {
	sseData := `event: message_start
data: {"type":"message_start","message":{"id":"msg_123","model":"claude-3-haiku-20240307","role":"assistant"}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"lookup","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":":\"SF\"}"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}

`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseData))
	}))
	defer srv.Close()

	p, _ := New("sk-test-key", srv.URL)
	ch, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "claude-3-haiku-20240307",
		Messages: []core.Message{{Role: core.RoleUser, Content: "weather?"}},
		Tools: []core.Tool{{
			Type: "function",
			Function: core.Function{
				Name: "lookup",
			},
		}},
		ToolChoice: "required",
	})
	if err != nil {
		t.Fatalf("CompleteStream() error: %v", err)
	}

	var chunks []core.StreamChunk
	for c := range ch {
		chunks = append(chunks, c)
	}

	if len(chunks) != 4 {
		t.Fatalf("chunks len = %d, want 4: %#v", len(chunks), chunks)
	}
	start := chunks[0].Choices[0].Delta.ToolCalls[0]
	if start.Index == nil || *start.Index != 0 || start.ID != "toolu_1" || start.Function.Name != "lookup" {
		t.Fatalf("start tool call = %#v, want lookup at index 0", start)
	}
	if chunks[1].Choices[0].Delta.ToolCalls[0].Function.Arguments != `{"city"` {
		t.Fatalf("first args delta = %#v", chunks[1].Choices[0].Delta.ToolCalls)
	}
	if chunks[2].Choices[0].Delta.ToolCalls[0].Function.Arguments != `:"SF"}` {
		t.Fatalf("second args delta = %#v", chunks[2].Choices[0].Delta.ToolCalls)
	}
	if chunks[3].Choices[0].FinishReason != core.FinishReasonToolCalls {
		t.Fatalf("finish_reason = %q, want %q", chunks[3].Choices[0].FinishReason, core.FinishReasonToolCalls)
	}
}

func TestAnthropicProvider_CompleteStream_MapsContentBlockIndexToToolCallIndex(t *testing.T) {
	sseData := `event: message_start
data: {"type":"message_start","message":{"id":"msg_123","model":"claude-3-haiku-20240307","role":"assistant"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Let me check."}}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"lookup","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"city\""}}

event: content_block_start
data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_2","name":"lookup_time","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"city\""}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}

`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseData))
	}))
	defer srv.Close()

	p, _ := New("sk-test-key", srv.URL)
	ch, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "claude-3-haiku-20240307",
		Messages: []core.Message{{Role: core.RoleUser, Content: "weather?"}},
	})
	if err != nil {
		t.Fatalf("CompleteStream() error: %v", err)
	}

	var chunks []core.StreamChunk
	for c := range ch {
		chunks = append(chunks, c)
	}

	if len(chunks) != 6 {
		t.Fatalf("chunks len = %d, want 6: %#v", len(chunks), chunks)
	}
	start := chunks[1].Choices[0].Delta.ToolCalls[0]
	if chunks[1].Choices[0].Index != 0 {
		t.Fatalf("choice index = %d, want sole completion index 0", chunks[1].Choices[0].Index)
	}
	if start.Index == nil || *start.Index != 0 {
		t.Fatalf("tool call index = %#v, want OpenAI tool index 0", start.Index)
	}
	args := chunks[2].Choices[0].Delta.ToolCalls[0]
	if args.Index == nil || *args.Index != 0 || args.Function.Arguments != `{"city"` {
		t.Fatalf("args delta = %#v, want tool index 0 with city fragment", args)
	}
	secondStart := chunks[3].Choices[0].Delta.ToolCalls[0]
	if chunks[3].Choices[0].Index != 0 {
		t.Fatalf("second choice index = %d, want sole completion index 0", chunks[3].Choices[0].Index)
	}
	if secondStart.Index == nil || *secondStart.Index != 1 {
		t.Fatalf("second tool call index = %#v, want OpenAI tool index 1", secondStart.Index)
	}
	secondArgs := chunks[4].Choices[0].Delta.ToolCalls[0]
	if chunks[4].Choices[0].Index != 0 {
		t.Fatalf("second args choice index = %d, want sole completion index 0", chunks[4].Choices[0].Index)
	}
	if secondArgs.Index == nil || *secondArgs.Index != 1 || secondArgs.Function.Arguments != `{"city"` {
		t.Fatalf("second args delta = %#v, want tool index 1 with city fragment", secondArgs)
	}
}

// TestAnthropicProvider_Complete_Integration tests actual API calls.
// This test only runs if ANTHROPIC_API_KEY environment variable is set.
func TestAnthropicProvider_Complete_Integration(t *testing.T) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("Skipping integration test: ANTHROPIC_API_KEY not set")
	}

	provider, err := New(apiKey, "")
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req := core.Request{
		Model: "claude-3-haiku-20240307",
		Messages: []core.Message{
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

func floatPtr(f float64) *float64 { return &f }
func intPtr(i int) *int           { return &i }
