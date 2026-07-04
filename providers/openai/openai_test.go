package openai

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
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
		{"codex family supported", "codex-mini-latest", true},
		{"sora family supported", "sora-2-pro", true},
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
		Model:  "gpt-4o-mini",
		Stream: true,
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
	if gotReq.Stream {
		t.Fatal("request stream = true, want false")
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

func TestOpenAIProvider_Complete_DrainsSuccessfulResponseBody(t *testing.T) {
	var newConnections atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-1",
			"object":"chat.completion",
			"created":1234567890,
			"model":"gpt-4o-mini",
			"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
		}`))
		_, _ = w.Write([]byte(strings.Repeat(" ", 1<<20)))
	}))
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			newConnections.Add(1)
		}
	}
	srv.Start()
	defer srv.Close()

	provider, err := New("sk-test-key", srv.URL)
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	provider.httpClient = &http.Client{Transport: transport}

	req := core.Request{
		Model:    "gpt-4o-mini",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	}
	for i := 0; i < 2; i++ {
		if _, err := provider.Complete(context.Background(), req); err != nil {
			t.Fatalf("Complete() call %d error: %v", i+1, err)
		}
	}

	if got := newConnections.Load(); got != 1 {
		t.Fatalf("new connections = %d, want 1; response body was not drained for reuse", got)
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
	var content strings.Builder
	for c := range ch {
		if c.Error != nil {
			t.Fatalf("stream error: %v", c.Error)
		}
		chunks = append(chunks, c)
		for _, choice := range c.Choices {
			content.WriteString(choice.Delta.Content)
		}
	}

	if len(chunks) != 4 {
		t.Fatalf("chunks len = %d, want 4: %#v", len(chunks), chunks)
	}
	for _, chunk := range chunks {
		if chunk.ID != "chatcmpl-1" {
			t.Errorf("chunk ID = %q, want chatcmpl-1", chunk.ID)
		}
	}
	if content.String() != "Hello world" {
		t.Errorf("assembled content = %q, want %q", content.String(), "Hello world")
	}
	if chunks[3].Choices[0].FinishReason != core.FinishReasonStop {
		t.Errorf("final finish_reason = %q, want stop", chunks[3].Choices[0].FinishReason)
	}
}

func TestOpenAIProvider_CompleteStream_ForwardsToolCallIndex(t *testing.T) {
	sseData := "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1234567890,\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"lookup\",\"arguments\":\"\"}}]},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1234567890,\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"city\\\"\"}}]},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1234567890,\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
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
		Messages: []core.Message{{Role: core.RoleUser, Content: "weather?"}},
		Tools: []core.Tool{{
			Type: "function",
			Function: core.Function{
				Name: "lookup",
			},
		}},
	})
	if err != nil {
		t.Fatalf("CompleteStream() error: %v", err)
	}

	var chunks []core.StreamChunk
	for c := range ch {
		if c.Error != nil {
			t.Fatalf("stream error: %v", c.Error)
		}
		chunks = append(chunks, c)
	}

	if len(chunks) != 3 {
		t.Fatalf("chunks len = %d, want 3: %#v", len(chunks), chunks)
	}
	start := chunks[0].Choices[0].Delta.ToolCalls[0]
	if start.Index == nil || *start.Index != 0 || start.ID != "call_1" || start.Function.Name != "lookup" {
		t.Fatalf("start tool call = %#v, want lookup at index 0", start)
	}
	delta := chunks[1].Choices[0].Delta.ToolCalls[0]
	if delta.Index == nil || *delta.Index != 0 || delta.Function.Arguments != `{"city"` {
		t.Fatalf("args delta = %#v, want index 0 city fragment", delta)
	}
	if chunks[2].Choices[0].FinishReason != core.FinishReasonToolCalls {
		t.Fatalf("finish_reason = %q, want %q", chunks[2].Choices[0].FinishReason, core.FinishReasonToolCalls)
	}
}

// ── Embed() validation tests ─────────────────────────────────────────────────

func TestOpenAIProvider_Embed_InvalidInputType(t *testing.T) {
	p, _ := New("sk-test", "")
	ctx := context.Background()

	badInputs := []struct {
		name  string
		input any
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

	_, err = p.Embed(ctx, core.EmbeddingRequest{Model: "text-embedding-3-small", Input: []any{}})
	if err == nil {
		t.Error("Embed() with empty []interface{}: expected error, got nil")
	}
}

func TestOpenAIProvider_Embed_SliceWithNonStringElement(t *testing.T) {
	p, _ := New("sk-test", "")
	ctx := context.Background()

	_, err := p.Embed(ctx, core.EmbeddingRequest{
		Model: "text-embedding-3-small",
		Input: []any{"valid", 99, "also-valid"},
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
		resp := map[string]any{
			"object": "list",
			"data": []map[string]any{
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

// TestOpenAIProvider_DiscoverModels_WireEndpoint verifies DiscoverModels hits
// <base>/v1/models and, critically, dedups a base URL that already ends in /v1
// (so it never issues /v1/v1/models). Bearer auth and the parsed model list are
// asserted too.
func TestOpenAIProvider_DiscoverModels_WireEndpoint(t *testing.T) {
	const wantPath = "/v1/models"

	cases := []struct {
		name       string
		baseSuffix string
	}{
		{"default base resolves to /v1/models", ""},
		{"trailing /v1 base is not doubled", "/v1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath, gotAuth string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotAuth = r.Header.Get("Authorization")
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"gpt-4o","object":"model","owned_by":"openai"},{"id":"gpt-4o-mini","object":"model"}]}`)
			}))
			defer srv.Close()

			p, err := New("sk-test-key", srv.URL+tc.baseSuffix)
			if err != nil {
				t.Fatalf("New() error: %v", err)
			}
			models, err := p.DiscoverModels(context.Background())
			if err != nil {
				t.Fatalf("DiscoverModels() error: %v", err)
			}
			if gotPath != wantPath {
				t.Errorf("request path = %q, want %q", gotPath, wantPath)
			}
			if gotAuth != "Bearer sk-test-key" {
				t.Errorf("Authorization = %q, want Bearer sk-test-key", gotAuth)
			}
			if len(models) != 2 {
				t.Fatalf("models len = %d, want 2", len(models))
			}
			if models[0].ID != "gpt-4o" || models[0].OwnedBy != "openai" {
				t.Errorf("models[0] = %+v, want gpt-4o owned_by openai", models[0])
			}
			// owned_by falls back to the provider name when absent.
			if models[1].OwnedBy != "openai" {
				t.Errorf("models[1] owned_by = %q, want openai fallback", models[1].OwnedBy)
			}
		})
	}
}

func floatPtr(f float64) *float64 { return &f }
func intPtr(i int) *int           { return &i }
