package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

func TestSanitizeRequestErr(t *testing.T) {
	urlErr := &url.Error{
		Op:  "Post",
		URL: "https://x/v1beta/models/m:predict?key=SECRETKEY",
		Err: errors.New("dial tcp: timeout"),
	}

	got := sanitizeRequestErr(urlErr).Error()
	if strings.Contains(got, "SECRETKEY") {
		t.Errorf("sanitizeRequestErr leaked API key: %q", got)
	}
	if !strings.Contains(got, "dial tcp") {
		t.Errorf("sanitizeRequestErr dropped underlying cause: %q", got)
	}

	// Non-url errors must pass through unchanged.
	plain := errors.New("boom")
	if sanitizeRequestErr(plain) != plain {
		t.Errorf("sanitizeRequestErr altered non-url error")
	}
}

func TestNewGemini(t *testing.T) {
	p, err := New("test-key", "")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if p.Name() != "gemini" {
		t.Errorf("Name() = %q, want gemini", p.Name())
	}
}

func TestGeminiProvider_SupportedModels(t *testing.T) {
	p, _ := New("test-key", "")
	models := p.SupportedModels()
	if len(models) == 0 {
		t.Error("SupportedModels() returned empty")
	}
	found := false
	foundEmbedding := false
	for _, m := range models {
		if m == "gemini-2.0-flash" {
			found = true
		}
		if m == "gemini-embedding-001" {
			foundEmbedding = true
		}
	}
	if !found {
		t.Error("gemini-2.0-flash not found")
	}
	if !foundEmbedding {
		t.Error("gemini-embedding-001 not found")
	}
}

func TestGeminiProvider_SupportsModel(t *testing.T) {
	p, _ := New("test-key", "")
	if !p.SupportsModel("gemini-2.0-flash") {
		t.Error("expected gemini-2.0-flash to be supported")
	}
	if p.SupportsModel("gpt-4o") {
		t.Error("gemini should not support gpt-4o")
	}
	if !p.SupportsModel("text-embedding-004") {
		t.Error("expected text-embedding-004 to be supported")
	}
}

func TestGeminiProvider_Models(t *testing.T) {
	p, _ := New("test-key", "")
	models := p.Models()
	for _, m := range models {
		if m.OwnedBy != "gemini" {
			t.Errorf("ModelInfo.OwnedBy = %q, want gemini", m.OwnedBy)
		}
	}
}

func TestGeminiProvider_CompleteStream_Interface(_ *testing.T) {
	p, _ := New("test-key", "")
	var _ core.StreamProvider = p
}

func TestGeminiProvider_Embed_Interface(_ *testing.T) {
	p, _ := New("test-key", "")
	var _ core.EmbeddingProvider = p
}

func TestGeminiProvider_Embed_BatchSuccess(t *testing.T) {
	dimensions := 64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/v1beta/models/gemini-embedding-001:batchEmbedContents" {
			t.Errorf("request path = %q, want /v1beta/models/gemini-embedding-001:batchEmbedContents", r.URL.Path)
		}
		if got := r.URL.Query().Get("key"); got != "test-key" {
			t.Errorf("key query = %q, want test-key", got)
		}
		var body struct {
			Requests []struct {
				Model   string `json:"model"`
				Content struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
				OutputDimensionality *int `json:"outputDimensionality"`
			} `json:"requests"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if len(body.Requests) != 2 {
			t.Fatalf("requests len = %d, want 2", len(body.Requests))
		}
		if body.Requests[0].Model != "models/gemini-embedding-001" || body.Requests[0].Content.Parts[0].Text != "first" {
			t.Errorf("first request = %+v", body.Requests[0])
		}
		if body.Requests[1].Content.Parts[0].Text != "second" {
			t.Errorf("second text = %q, want second", body.Requests[1].Content.Parts[0].Text)
		}
		if body.Requests[0].OutputDimensionality == nil || *body.Requests[0].OutputDimensionality != dimensions {
			t.Errorf("outputDimensionality = %v, want %d", body.Requests[0].OutputDimensionality, dimensions)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"embeddings":[{"values":[0.1,0.2]},{"values":[0.3,0.4]}],"usageMetadata":{"promptTokenCount":7,"totalTokenCount":7}}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	resp, err := p.Embed(context.Background(), core.EmbeddingRequest{
		Model:      "gemini-embedding-001",
		Input:      []string{"first", "second"},
		Dimensions: &dimensions,
	})
	if err != nil {
		t.Fatalf("Embed() error: %v", err)
	}
	if resp.Object != "list" || resp.Model != "gemini-embedding-001" {
		t.Errorf("response metadata = (%q, %q)", resp.Object, resp.Model)
	}
	if len(resp.Data) != 2 || resp.Data[0].Index != 0 || resp.Data[1].Index != 1 {
		t.Fatalf("response data = %+v", resp.Data)
	}
	if resp.Data[0].Embedding[0] != 0.1 || resp.Data[1].Embedding[1] != 0.4 {
		t.Errorf("embeddings = %+v", resp.Data)
	}
	if resp.Usage.PromptTokens != 7 || resp.Usage.TotalTokens != 7 {
		t.Errorf("usage = %+v, want 7 prompt/total", resp.Usage)
	}
}

func TestGeminiProvider_Embed_StringInput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Requests []struct {
				Content struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
			} `json:"requests"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if len(body.Requests) != 1 || body.Requests[0].Content.Parts[0].Text != "hello" {
			t.Fatalf("request body = %+v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"embeddings":[{"values":[1,2,3]}]}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	resp, err := p.Embed(context.Background(), core.EmbeddingRequest{
		Model: "text-embedding-004",
		Input: "hello",
	})
	if err != nil {
		t.Fatalf("Embed() error: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].Embedding[2] != 3 {
		t.Errorf("response data = %+v", resp.Data)
	}
}

func TestGeminiProvider_Embed_InvalidInput(t *testing.T) {
	p, _ := New("test-key", "")
	_, err := p.Embed(context.Background(), core.EmbeddingRequest{
		Model: "gemini-embedding-001",
		Input: []any{"ok", 123},
	})
	if err == nil || !strings.Contains(err.Error(), "Input[1]") {
		t.Fatalf("Embed() error = %v, want invalid input error", err)
	}
}

func TestGeminiProvider_Embed_UpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad embedding request"}}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	_, err := p.Embed(context.Background(), core.EmbeddingRequest{
		Model: "gemini-embedding-001",
		Input: "hello",
	})
	if err == nil || !strings.Contains(err.Error(), "bad embedding request") {
		t.Fatalf("Embed() error = %v, want upstream error", err)
	}
}

func TestGeminiProvider_CompleteStream_MockSSE(t *testing.T) {
	sseData := "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Hello\"}],\"role\":\"model\"},\"finishReason\":\"\"}]}\n\n" +
		"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\" there\"}],\"role\":\"model\"},\"finishReason\":\"\"}]}\n\n" +
		"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"!\"}],\"role\":\"model\"},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":5,\"candidatesTokenCount\":3,\"totalTokenCount\":8}}\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseData))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	ch, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "gemini-2.0-flash",
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
	if chunks[0].Choices[0].Delta.Content != "Hello" {
		t.Errorf("delta content = %q, want Hello", chunks[0].Choices[0].Delta.Content)
	}
	if chunks[1].Choices[0].Delta.Content != " there" {
		t.Errorf("delta content = %q, want ' there'", chunks[1].Choices[0].Delta.Content)
	}
}

func TestGeminiProvider_CompleteStream_IndexesFunctionCalls(t *testing.T) {
	sseData := `data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"call_1","name":"lookup_weather","args":{"city":"SF"}}},{"functionCall":{"id":"call_2","name":"lookup_time","args":{"city":"SF"}}}]},"finishReason":"STOP"}]}

`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseData))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	ch, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "gemini-2.0-flash",
		Messages: []core.Message{{Role: core.RoleUser, Content: "weather and time?"}},
	})
	if err != nil {
		t.Fatalf("CompleteStream() error: %v", err)
	}

	var chunks []core.StreamChunk
	for c := range ch {
		chunks = append(chunks, c)
	}

	if len(chunks) != 1 || len(chunks[0].Choices) != 1 {
		t.Fatalf("chunks = %#v, want one candidate chunk", chunks)
	}
	toolCalls := chunks[0].Choices[0].Delta.ToolCalls
	if len(toolCalls) != 2 {
		t.Fatalf("tool calls len = %d, want 2", len(toolCalls))
	}
	if toolCalls[0].Index == nil || *toolCalls[0].Index != 0 {
		t.Fatalf("first tool index = %#v, want 0", toolCalls[0].Index)
	}
	if toolCalls[1].Index == nil || *toolCalls[1].Index != 1 {
		t.Fatalf("second tool index = %#v, want 1", toolCalls[1].Index)
	}
	if chunks[0].Choices[0].FinishReason != core.FinishReasonToolCalls {
		t.Fatalf("finish reason = %q, want tool_calls", chunks[0].Choices[0].FinishReason)
	}
}

func TestGeminiProvider_Complete_ForwardsToolsAndDecodesFunctionCall(t *testing.T) {
	var captured map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"candidates":[{
				"content":{"role":"model","parts":[{"functionCall":{"id":"call_1","name":"lookup","args":{"city":"SF"}}}]},
				"finishReason":"STOP"
			}],
			"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}
		}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	resp, err := p.Complete(context.Background(), core.Request{
		Model:    "gemini-2.0-flash",
		Messages: []core.Message{{Role: core.RoleUser, Content: "weather?"}},
		Tools: []core.Tool{{
			Type: "function",
			Function: core.Function{
				Name:        "lookup",
				Description: "Lookup weather",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`),
			},
		}},
		ToolChoice: "required",
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}

	var body struct {
		Tools []struct {
			FunctionDeclarations []struct {
				Name string `json:"name"`
			} `json:"functionDeclarations"`
		} `json:"tools"`
		ToolConfig struct {
			FunctionCallingConfig struct {
				Mode string `json:"mode"`
			} `json:"functionCallingConfig"`
		} `json:"toolConfig"`
	}
	raw, _ := json.Marshal(captured)
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode captured body: %v", err)
	}
	if len(body.Tools) != 1 || len(body.Tools[0].FunctionDeclarations) != 1 || body.Tools[0].FunctionDeclarations[0].Name != "lookup" {
		t.Fatalf("tools = %#v, want lookup", body.Tools)
	}
	if body.ToolConfig.FunctionCallingConfig.Mode != "ANY" {
		t.Fatalf("tool config mode = %q, want ANY", body.ToolConfig.FunctionCallingConfig.Mode)
	}
	if len(resp.Choices) != 1 || len(resp.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %#v, want one", resp.Choices)
	}
	tc := resp.Choices[0].Message.ToolCalls[0]
	if tc.ID != "call_1" || tc.Function.Name != "lookup" || tc.Function.Arguments != `{"city":"SF"}` {
		t.Fatalf("tool call = %#v, want lookup", tc)
	}
	if resp.Choices[0].FinishReason != core.FinishReasonToolCalls {
		t.Fatalf("finish reason = %q, want tool_calls", resp.Choices[0].FinishReason)
	}
}

func TestGeminiProvider_Complete_ForwardsToolResultWithFunctionName(t *testing.T) {
	var captured struct {
		Contents []struct {
			Role  string `json:"role"`
			Parts []struct {
				FunctionResponse *struct {
					ID       string          `json:"id"`
					Name     string          `json:"name"`
					Response json.RawMessage `json:"response"`
				} `json:"functionResponse"`
			} `json:"parts"`
		} `json:"contents"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"done"}]}}]}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	_, err := p.Complete(context.Background(), core.Request{
		Model: "gemini-2.0-flash",
		Messages: []core.Message{
			{Role: core.RoleUser, Content: "weather?"},
			{Role: core.RoleAssistant, ToolCalls: []core.ToolCall{{
				ID:   "call_1",
				Type: "function",
				Function: core.FunctionCall{
					Name:      "lookup",
					Arguments: `{"city":"SF"}`,
				},
			}}},
			{Role: core.RoleTool, ToolCallID: "call_1", Content: `{"temp":72}`},
		},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}

	if len(captured.Contents) != 3 {
		t.Fatalf("contents len = %d, want 3", len(captured.Contents))
	}
	toolResult := captured.Contents[2]
	if toolResult.Role != core.RoleUser || len(toolResult.Parts) != 1 || toolResult.Parts[0].FunctionResponse == nil {
		t.Fatalf("tool result content = %#v, want user functionResponse", toolResult)
	}
	got := toolResult.Parts[0].FunctionResponse
	if got.ID != "call_1" || got.Name != "lookup" || string(got.Response) != `{"temp":72}` {
		t.Fatalf("function response = %#v, want lookup call_1", got)
	}
}

func TestGeminiProvider_Complete_WrapsNonObjectToolResult(t *testing.T) {
	var captured struct {
		Contents []struct {
			Role  string `json:"role"`
			Parts []struct {
				FunctionResponse *struct {
					ID       string          `json:"id"`
					Name     string          `json:"name"`
					Response json.RawMessage `json:"response"`
				} `json:"functionResponse"`
			} `json:"parts"`
		} `json:"contents"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"done"}]}}]}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	_, err := p.Complete(context.Background(), core.Request{
		Model: "gemini-2.0-flash",
		Messages: []core.Message{
			{Role: core.RoleUser, Content: "status?"},
			{Role: core.RoleAssistant, ToolCalls: []core.ToolCall{{
				ID:   "call_1",
				Type: "function",
				Function: core.FunctionCall{
					Name:      "lookup",
					Arguments: `{}`,
				},
			}}},
			{Role: core.RoleTool, ToolCallID: "call_1", Content: `"ok"`},
		},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}

	if len(captured.Contents) != 3 {
		t.Fatalf("contents len = %d, want 3", len(captured.Contents))
	}
	toolResult := captured.Contents[2]
	if toolResult.Role != core.RoleUser || len(toolResult.Parts) != 1 || toolResult.Parts[0].FunctionResponse == nil {
		t.Fatalf("tool result content = %#v, want user functionResponse", toolResult)
	}
	got := toolResult.Parts[0].FunctionResponse
	if got.ID != "call_1" || got.Name != "lookup" || string(got.Response) != `{"result":"ok"}` {
		t.Fatalf("function response = %#v, want wrapped lookup call_1", got)
	}
}
