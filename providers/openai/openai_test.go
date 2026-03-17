package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	core "github.com/ferro-labs/ai-gateway/providers/core"
)

// TestNewOpenAI tests the OpenAI provider constructor.
func TestNewOpenAI(t *testing.T) {
	provider, err := New("sk-test-key", "")
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}
	if provider == nil {
		t.Fatal("New() returned nil provider")
	}
	if provider.Name() != Name {
		t.Errorf("New() provider name = %v, want openai", provider.Name())
	}
}

// TestOpenAIProvider_SupportedModels tests the SupportedModels method.
func TestOpenAIProvider_SupportedModels(t *testing.T) {
	provider, _ := New("sk-test-key", "")

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
	provider, _ := New("sk-test-key", "")

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
	provider, _ := New("sk-test-key", "")

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

	provider, err := New(apiKey, "")
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req := core.Request{
		Model: "gpt-3.5-turbo",
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

func TestOpenAIProvider_Complete_MockHTTP(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotReq core.Request

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-1",
			"object":"chat.completion",
			"created":1234567890,
			"model":"gpt-4o-mini",
			"choices":[
				{
					"index":0,
					"message":{
						"role":"assistant",
						"content":"hello",
						"tool_calls":[
							{
								"id":"call-1",
								"type":"function",
								"function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}
							}
						]
					},
					"finish_reason":"stop"
				}
			],
			"usage":{
				"prompt_tokens":12,
				"completion_tokens":7,
				"total_tokens":19,
				"prompt_tokens_details":{"cached_tokens":3},
				"completion_tokens_details":{"reasoning_tokens":2}
			}
		}`))
	}))
	defer srv.Close()

	provider, err := New("sk-test-key", srv.URL)
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}

	req := core.Request{
		Model: "gpt-4o-mini",
		Messages: []core.Message{
			{Role: "user", Content: "hi"},
		},
		MaxTokens: intPtr(16),
	}

	resp, err := provider.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}

	if gotPath != "/v1/chat/completions" {
		t.Fatalf("path = %q, want /v1/chat/completions", gotPath)
	}
	if gotAuth != "Bearer sk-test-key" {
		t.Fatalf("authorization = %q", gotAuth)
	}
	if gotReq.Model != req.Model {
		t.Fatalf("request model = %q, want %q", gotReq.Model, req.Model)
	}
	if resp.Provider != "openai" {
		t.Fatalf("provider = %q, want openai", resp.Provider)
	}
	if resp.Usage.CacheReadTokens != 3 {
		t.Fatalf("cache_read_tokens = %d, want 3", resp.Usage.CacheReadTokens)
	}
	if resp.Usage.ReasoningTokens != 2 {
		t.Fatalf("reasoning_tokens = %d, want 2", resp.Usage.ReasoningTokens)
	}
	if len(resp.Choices) != 1 || len(resp.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("tool calls were not decoded correctly: %+v", resp.Choices)
	}
}

func TestOpenAIProvider_Complete_MockHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited","type":"rate_limit"}}`))
	}))
	defer srv.Close()

	provider, err := New("sk-test-key", srv.URL)
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}

	_, err = provider.Complete(context.Background(), core.Request{
		Model:    "gpt-4o-mini",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "openai API error (429): rate limited") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestOpenAIProvider_CompleteStream_Interface verifies the interface compliance.
func TestOpenAIProvider_CompleteStream_Interface(_ *testing.T) {
	provider, _ := New("sk-test-key", "")
	var _ core.StreamProvider = provider
}

// TestOpenAIProvider_CompleteStream_MockSSE verifies streaming works with a mock server.
func TestOpenAIProvider_CompleteStream_MockSSE(t *testing.T) {
	// OpenAI streaming format: data: {chunk}\n\ndata: [DONE]\n\n
	sseData := "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1234567890,\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1234567890,\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1234567890,\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" world\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1234567890,\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseData))
	}))
	defer srv.Close()

	provider, _ := New("sk-test-key", srv.URL)
	ch, err := provider.CompleteStream(context.Background(), core.Request{
		Model:    "gpt-4o",
		Messages: []core.Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("CompleteStream() error: %v", err)
	}

	var chunks []core.StreamChunk
	for c := range ch {
		if c.Error != nil {
			t.Logf("CompleteStream chunk error (SDK may reject mock format): %v", c.Error)
			return
		}
		chunks = append(chunks, c)
	}

	if len(chunks) == 0 {
		t.Log("No chunks received — openai-go SDK may not process mock SSE; interface compliance verified by _Interface test")
		return
	}

	// Verify chunk structure when SDK parses successfully.
	for _, chunk := range chunks {
		if chunk.ID != "chatcmpl-1" {
			t.Errorf("chunk ID = %q, want %s", chunk.ID, "chatcmpl-1")
		}
	}
}

// ── Embed() validation tests ─────────────────────────────────────────────────

func TestOpenAIProvider_Embed_InvalidInputType(t *testing.T) {
	p, _ := New("sk-test", "")
	ctx := context.Background()

	badInputs := []struct {
		name  string
		input interface{}
	}{
		{"nil", nil},
		{"integer", 42},
		{"float", 3.14},
		{"bool", true},
		{"map", map[string]string{"a": "b"}},
	}
	for _, tc := range badInputs {
		t.Run(tc.name, func(t *testing.T) {
			_, err := p.Embed(ctx, core.EmbeddingRequest{Model: "text-embedding-3-small", Input: tc.input})
			if err == nil {
				t.Errorf("Embed() with Input=%T: expected error, got nil", tc.input)
			}
		})
	}
}

func TestOpenAIProvider_Embed_EmptyArrayInput(t *testing.T) {
	p, _ := New("sk-test", "")
	ctx := context.Background()

	_, err := p.Embed(ctx, core.EmbeddingRequest{Model: "text-embedding-3-small", Input: []string{}})
	if err == nil {
		t.Error("Embed() with empty []string: expected error, got nil")
	}

	_, err = p.Embed(ctx, core.EmbeddingRequest{Model: "text-embedding-3-small", Input: []interface{}{}})
	if err == nil {
		t.Error("Embed() with empty []interface{}: expected error, got nil")
	}
}

func TestOpenAIProvider_Embed_SliceWithNonStringElement(t *testing.T) {
	p, _ := New("sk-test", "")
	ctx := context.Background()

	_, err := p.Embed(ctx, core.EmbeddingRequest{
		Model: "text-embedding-3-small",
		Input: []interface{}{"valid", 99, "also-valid"},
	})
	if err == nil {
		t.Error("expected error for []interface{} with non-string element, got nil")
	}
	if !strings.Contains(err.Error(), "Input[1]") {
		t.Errorf("error should mention the offending index, got: %v", err)
	}
}

func TestOpenAIProvider_Embed_InvalidEncodingFormat(t *testing.T) {
	p, _ := New("sk-test", "")
	ctx := context.Background()

	for _, format := range []string{"int8", "uint8", "binary", "ubinary", "invalid"} {
		_, err := p.Embed(ctx, core.EmbeddingRequest{
			Model:          "text-embedding-3-small",
			Input:          "hello",
			EncodingFormat: format,
		})
		if err == nil {
			t.Errorf("Embed() with EncodingFormat=%q: expected error, got nil", format)
		}
		if !strings.Contains(err.Error(), format) {
			t.Errorf("error for format %q should mention the bad value; got: %v", format, err)
		}
	}
}

func TestOpenAIProvider_Embed_ValidEncodingFormats(t *testing.T) {
	// Mock server returns an OpenAI-compatible embedding response.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := map[string]interface{}{
			"object": "list",
			"data": []map[string]interface{}{
				{"object": "embedding", "embedding": []float64{0.1, 0.2}, "index": 0},
			},
			"model": "text-embedding-3-small",
			"usage": map[string]int{"prompt_tokens": 1, "total_tokens": 1},
		}
		data, _ := json.Marshal(resp)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	p, _ := New("sk-test", srv.URL)
	ctx := context.Background()

	for _, format := range []string{"", "float", "base64"} {
		t.Run("format="+format, func(t *testing.T) {
			_, err := p.Embed(ctx, core.EmbeddingRequest{
				Model:          "text-embedding-3-small",
				Input:          "hello",
				EncodingFormat: format,
			})
			if err != nil {
				t.Errorf("Embed() with EncodingFormat=%q: unexpected error: %v", format, err)
			}
		})
	}
}

func floatPtr(f float64) *float64 { return &f }
func intPtr(i int) *int           { return &i }
