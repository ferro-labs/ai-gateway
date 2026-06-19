package cohere

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

func TestNewCohere(t *testing.T) {
	p, err := New("test-key", "")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if p.Name() != "cohere" {
		t.Errorf("Name() = %q, want cohere", p.Name())
	}
}

func TestCohereProvider_SupportedModels(t *testing.T) {
	p, _ := New("test-key", "")
	models := p.SupportedModels()
	if len(models) == 0 {
		t.Error("SupportedModels() returned empty")
	}
	wantModels := []string{"command-r-plus", "embed-v4.0", "embed-english-v3.0", "embed-multilingual-v3.0"}
	found := map[string]bool{}
	for _, m := range models {
		found[m] = true
	}
	for _, want := range wantModels {
		if !found[want] {
			t.Errorf("%s not found", want)
		}
	}
}

func TestCohereProvider_SupportsModel(t *testing.T) {
	p, _ := New("test-key", "")
	if !p.SupportsModel("command-r-plus") {
		t.Error("expected command-r-plus to be supported")
	}
	if !p.SupportsModel("command") {
		t.Error("expected command to be supported")
	}
	if p.SupportsModel("gpt-4o") {
		t.Error("cohere should not support gpt-4o")
	}
}

func TestCohereProvider_Models(t *testing.T) {
	p, _ := New("test-key", "")
	models := p.Models()
	for _, m := range models {
		if m.OwnedBy != "cohere" {
			t.Errorf("ModelInfo.OwnedBy = %q, want cohere", m.OwnedBy)
		}
	}
}

func TestCohereProvider_CompleteStream_Interface(_ *testing.T) {
	p, _ := New("test-key", "")
	var _ core.StreamProvider = p
}

func TestCohereProvider_EmbeddingProvider_Interface(_ *testing.T) {
	p, _ := New("test-key", "")
	var _ core.EmbeddingProvider = p
}

func TestCohereProvider_CompleteStream_MockSSE(t *testing.T) {
	sseData := "data: {\"type\":\"content-delta\",\"delta\":{\"message\":{\"content\":{\"text\":\"Hello\"}}}}\n\n" +
		"data: {\"type\":\"content-delta\",\"delta\":{\"message\":{\"content\":{\"text\":\" there\"}}}}\n\n" +
		"data: {\"type\":\"message-end\",\"delta\":{\"finish_reason\":\"COMPLETE\",\"usage\":{\"billed_units\":{\"input_tokens\":5,\"output_tokens\":2},\"tokens\":{\"input_tokens\":5,\"output_tokens\":2}}}}\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseData))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	ch, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "command-r-plus",
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
	//nolint:goconst // " there" appears in multiple test strings; fine in tests
	if chunks[1].Choices[0].Delta.Content != " there" {
		t.Errorf("delta content = %q, want ' there'", chunks[1].Choices[0].Delta.Content)
	}
	// COMPLETE is normalized to the OpenAI-canonical "stop" (#142).
	if chunks[2].Choices[0].FinishReason != "stop" {
		t.Errorf("finish_reason = %q, want stop", chunks[2].Choices[0].FinishReason)
	}
}

func TestCohereProvider_CompleteStream_ForwardsToolCallDeltas(t *testing.T) {
	sseData := `data: {"type":"message-start","id":"chat-1","delta":{"message":{"role":"assistant"}}}

data: {"type":"tool-call-start","index":0,"delta":{"message":{"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":""}}]}}}

data: {"type":"tool-call-delta","index":0,"delta":{"message":{"tool_calls":{"function":{"arguments":"{\"city\""}}}}}

data: {"type":"tool-call-delta","index":0,"delta":{"message":{"tool_calls":{"function":{"arguments":":\"SF\"}"}}}}}

data: {"type":"tool-call-end","index":0}

data: {"type":"tool-call-start","index":1,"delta":{"message":{"tool_calls":[{"id":"call_2","type":"function","function":{"name":"lookup_time","arguments":""}}]}}}

data: {"type":"tool-call-delta","index":1,"delta":{"message":{"tool_calls":{"function":{"arguments":"{\"city\""}}}}}

data: {"type":"tool-call-end","index":1}

data: {"type":"message-end","delta":{"finish_reason":"TOOL_CALL","usage":{"tokens":{"input_tokens":5,"output_tokens":2}}}}

`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseData))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	ch, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "command-r-plus",
		Messages: []core.Message{{Role: core.RoleUser, Content: "weather?"}},
		Tools: []core.Tool{{
			Type: "function",
			Function: core.Function{
				Name:       "lookup",
				Parameters: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`),
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

	if len(chunks) != 6 {
		t.Fatalf("chunks len = %d, want 6: %#v", len(chunks), chunks)
	}
	start := chunks[0].Choices[0].Delta.ToolCalls[0]
	if chunks[0].Choices[0].Index != 0 {
		t.Fatalf("choice index = %d, want sole completion index 0", chunks[0].Choices[0].Index)
	}
	if start.Index == nil || *start.Index != 0 || start.ID != "call_1" || start.Function.Name != "lookup" {
		t.Fatalf("start tool call = %#v, want lookup call at index 0", start)
	}
	if chunks[1].Choices[0].Index != 0 {
		t.Fatalf("args choice index = %d, want sole completion index 0", chunks[1].Choices[0].Index)
	}
	if chunks[1].Choices[0].Delta.ToolCalls[0].Function.Arguments != `{"city"` {
		t.Fatalf("first args delta = %#v", chunks[1].Choices[0].Delta.ToolCalls)
	}
	if chunks[2].Choices[0].Index != 0 {
		t.Fatalf("second args choice index = %d, want sole completion index 0", chunks[2].Choices[0].Index)
	}
	if chunks[2].Choices[0].Delta.ToolCalls[0].Function.Arguments != `:"SF"}` {
		t.Fatalf("second args delta = %#v", chunks[2].Choices[0].Delta.ToolCalls)
	}
	secondStart := chunks[3].Choices[0].Delta.ToolCalls[0]
	if chunks[3].Choices[0].Index != 0 {
		t.Fatalf("second tool choice index = %d, want sole completion index 0", chunks[3].Choices[0].Index)
	}
	if secondStart.Index == nil || *secondStart.Index != 1 || secondStart.ID != "call_2" || secondStart.Function.Name != "lookup_time" {
		t.Fatalf("second start tool call = %#v, want lookup_time call at index 1", secondStart)
	}
	secondArgs := chunks[4].Choices[0].Delta.ToolCalls[0]
	if chunks[4].Choices[0].Index != 0 {
		t.Fatalf("second tool args choice index = %d, want sole completion index 0", chunks[4].Choices[0].Index)
	}
	if secondArgs.Index == nil || *secondArgs.Index != 1 || secondArgs.Function.Arguments != `{"city"` {
		t.Fatalf("second tool args delta = %#v, want index 1 city fragment", secondArgs)
	}
	if chunks[5].Choices[0].FinishReason != core.FinishReasonToolCalls {
		t.Fatalf("finish_reason = %q, want %q", chunks[5].Choices[0].FinishReason, core.FinishReasonToolCalls)
	}
}

func TestCohereProvider_Complete_ForwardsToolsAndDecodesToolCalls(t *testing.T) {
	var captured cohereRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"chat-1",
			"finish_reason":"TOOL_CALL",
			"message":{
				"role":"assistant",
				"content":[],
				"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"city\":\"SF\"}"}}]
			},
			"usage":{"tokens":{"input_tokens":1,"output_tokens":1}}
		}`)
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	resp, err := p.Complete(context.Background(), core.Request{
		Model:    "command-r-plus",
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
	if len(captured.Tools) != 1 || captured.Tools[0].Function.Name != "lookup" {
		t.Fatalf("tools = %#v, want lookup", captured.Tools)
	}
	if captured.ToolChoice != "REQUIRED" {
		t.Fatalf("tool_choice = %q, want REQUIRED", captured.ToolChoice)
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

func TestCohereProvider_Complete_ForwardsToolResultAndDecodesFinalAnswer(t *testing.T) {
	var captured struct {
		Messages []struct {
			Role       string          `json:"role"`
			ToolCallID string          `json:"tool_call_id,omitempty"`
			Content    json.RawMessage `json:"content,omitempty"`
			ToolCalls  []core.ToolCall `json:"tool_calls,omitempty"`
		} `json:"messages"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"chat-2",
			"finish_reason":"COMPLETE",
			"message":{
				"role":"assistant",
				"content":[{"type":"text","text":"It is 72F in SF."}]
			},
			"usage":{"tokens":{"input_tokens":2,"output_tokens":3}}
		}`)
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	resp, err := p.Complete(context.Background(), core.Request{
		Model: "command-r-plus",
		Messages: []core.Message{
			{Role: core.RoleUser, Content: "weather?"},
			{Role: core.RoleAssistant, ToolCalls: []core.ToolCall{{
				ID:       "call_1",
				Type:     "function",
				Function: core.FunctionCall{Name: "lookup", Arguments: `{"city":"SF"}`},
			}}},
			{Role: core.RoleTool, ToolCallID: "call_1", Content: `{"temp":"72F"}`},
		},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}

	if len(captured.Messages) != 3 {
		t.Fatalf("messages len = %d, want 3", len(captured.Messages))
	}
	if len(captured.Messages[1].ToolCalls) != 1 || captured.Messages[1].ToolCalls[0].ID != "call_1" {
		t.Fatalf("assistant tool calls = %#v, want call_1", captured.Messages[1].ToolCalls)
	}
	if captured.Messages[2].Role != core.RoleTool || captured.Messages[2].ToolCallID != "call_1" {
		t.Fatalf("tool result message = %#v, want role tool with call_1", captured.Messages[2])
	}
	var toolContent []struct {
		Type     string `json:"type"`
		Document struct {
			Data string `json:"data"`
		} `json:"document"`
	}
	if err := json.Unmarshal(captured.Messages[2].Content, &toolContent); err != nil {
		t.Fatalf("decode tool content: %v", err)
	}
	if len(toolContent) != 1 ||
		toolContent[0].Type != "document" ||
		toolContent[0].Document.Data != `{"temp":"72F"}` {
		t.Fatalf("tool result content = %#v, want document block", toolContent)
	}
	if got := resp.Choices[0].Message.Content; got != "It is 72F in SF." {
		t.Fatalf("final answer = %q, want weather answer", got)
	}
	if len(resp.Choices[0].Message.ToolCalls) != 0 {
		t.Fatalf("final answer tool calls = %#v, want none", resp.Choices[0].Message.ToolCalls)
	}
}

func TestCohereProvider_Embed_StringInput_MockHTTP(t *testing.T) {
	const apiKey = "test-key"
	seenRequest := false

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenRequest = true
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/v1/embed" {
			t.Errorf("request path = %q, want /v1/embed", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+apiKey {
			t.Errorf("Authorization = %q, want Bearer %s", got, apiKey)
		}

		var got cohereEmbedRequest
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("failed to decode request: %v", err)
		}
		if got.Model != "embed-english-v3.0" {
			t.Errorf("model = %q, want embed-english-v3.0", got.Model)
		}
		if !reflect.DeepEqual(got.Texts, []string{"hello"}) {
			t.Errorf("texts = %#v, want [hello]", got.Texts)
		}
		if got.InputType != "search_document" {
			t.Errorf("input_type = %q, want search_document", got.InputType)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"embed-1","embeddings":[[0.1,0.2]],"texts":["hello"],"meta":{"billed_units":{"input_tokens":3}}}`)
	}))
	defer srv.Close()

	p, _ := New(apiKey, srv.URL)
	resp, err := p.Embed(context.Background(), core.EmbeddingRequest{
		Model: "embed-english-v3.0",
		Input: "hello",
	})
	if err != nil {
		t.Fatalf("Embed() error: %v", err)
	}
	if !seenRequest {
		t.Fatal("expected test server to receive request")
	}
	if resp.Object != "list" {
		t.Errorf("Object = %q, want list", resp.Object)
	}
	if resp.Model != "embed-english-v3.0" {
		t.Errorf("Model = %q, want embed-english-v3.0", resp.Model)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("Data length = %d, want 1", len(resp.Data))
	}
	if resp.Data[0].Object != "embedding" {
		t.Errorf("Data[0].Object = %q, want embedding", resp.Data[0].Object)
	}
	if resp.Data[0].Index != 0 {
		t.Errorf("Data[0].Index = %d, want 0", resp.Data[0].Index)
	}
	if !reflect.DeepEqual(resp.Data[0].Embedding, []float64{0.1, 0.2}) {
		t.Errorf("Data[0].Embedding = %#v, want [0.1 0.2]", resp.Data[0].Embedding)
	}
	if resp.Usage.PromptTokens != 3 || resp.Usage.TotalTokens != 3 {
		t.Errorf("Usage = %+v, want prompt_tokens=3 total_tokens=3", resp.Usage)
	}
}

func TestCohereProvider_Embed_StringSliceInput_MockHTTP(t *testing.T) {
	var gotTexts []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embed" {
			t.Errorf("request path = %q, want /v1/embed", r.URL.Path)
		}

		var got cohereEmbedRequest
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("failed to decode request: %v", err)
		}
		gotTexts = got.Texts
		if got.Model != "embed-multilingual-v3.0" {
			t.Errorf("model = %q, want embed-multilingual-v3.0", got.Model)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"embed-2","embeddings":[[0.1,0.2],[0.3,0.4]],"texts":["hello","there"],"meta":{"billed_units":{"input_tokens":5}}}`)
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	resp, err := p.Embed(context.Background(), core.EmbeddingRequest{
		Model: "embed-multilingual-v3.0",
		Input: []string{"hello", "there"},
	})
	if err != nil {
		t.Fatalf("Embed() error: %v", err)
	}
	if !reflect.DeepEqual(gotTexts, []string{"hello", "there"}) {
		t.Errorf("texts = %#v, want [hello there]", gotTexts)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("Data length = %d, want 2", len(resp.Data))
	}
	for i := range resp.Data {
		if resp.Data[i].Index != i {
			t.Errorf("Data[%d].Index = %d, want %d", i, resp.Data[i].Index, i)
		}
	}
	if resp.Usage.PromptTokens != 5 || resp.Usage.TotalTokens != 5 {
		t.Errorf("Usage = %+v, want prompt_tokens=5 total_tokens=5", resp.Usage)
	}
}

func TestCohereProvider_Embed_InvalidInputType(t *testing.T) {
	p, _ := New("test-key", "")
	badInputs := []struct {
		name  string
		input any
	}{
		{"nil", nil},
		{"integer", 42},
		{"float", 3.14},
		{"bool", true},
		{"map", map[string]string{"hello": "there"}},
	}

	for _, tc := range badInputs {
		t.Run(tc.name, func(t *testing.T) {
			_, err := p.Embed(context.Background(), core.EmbeddingRequest{
				Model: "embed-english-v3.0",
				Input: tc.input,
			})
			if err == nil {
				t.Fatalf("Embed() with Input=%T: expected error, got nil", tc.input)
			}
			if !strings.Contains(err.Error(), "unsupported input type") {
				t.Errorf("error = %q, want unsupported input type", err.Error())
			}
		})
	}
}

func TestCohereProvider_Embed_SliceWithNonStringElement(t *testing.T) {
	p, _ := New("test-key", "")
	_, err := p.Embed(context.Background(), core.EmbeddingRequest{
		Model: "embed-english-v3.0",
		Input: []any{"valid", 99, "also-valid"},
	})
	if err == nil {
		t.Fatal("expected error for []interface{} with non-string element, got nil")
	}
	if !strings.Contains(err.Error(), "input[1]") || !strings.Contains(err.Error(), "expected string") {
		t.Errorf("error = %q, want offending index and expected type", err.Error())
	}
}

func TestCohereProvider_Embed_EmptyInput(t *testing.T) {
	p, _ := New("test-key", "")
	inputs := []struct {
		name  string
		input any
	}{
		{"empty string slice", []string{}},
		{"empty interface slice", []any{}},
	}

	for _, tc := range inputs {
		t.Run(tc.name, func(t *testing.T) {
			_, err := p.Embed(context.Background(), core.EmbeddingRequest{
				Model: "embed-english-v3.0",
				Input: tc.input,
			})
			if err == nil {
				t.Fatal("expected error for empty input, got nil")
			}
			if !strings.Contains(err.Error(), "at least one text") {
				t.Errorf("error = %q, want at least one text", err.Error())
			}
		})
	}
}

func TestCohereProvider_Embed_UnsupportedOptionalFields(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	dimensions := 256
	p, _ := New("test-key", srv.URL)
	tests := []struct {
		name    string
		req     core.EmbeddingRequest
		wantErr string
	}{
		{
			name: "unsupported encoding format",
			req: core.EmbeddingRequest{
				Model:          "embed-english-v3.0",
				Input:          "hello",
				EncodingFormat: "base64",
			},
			wantErr: "unsupported encoding_format",
		},
		{
			name: "dimensions",
			req: core.EmbeddingRequest{
				Model:      "embed-english-v3.0",
				Input:      "hello",
				Dimensions: &dimensions,
			},
			wantErr: "dimensions are not supported",
		},
		{
			name: "user",
			req: core.EmbeddingRequest{
				Model: "embed-english-v3.0",
				Input: "hello",
				User:  "user-123",
			},
			wantErr: "user is not supported",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := p.Embed(context.Background(), tc.req)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want %q", err.Error(), tc.wantErr)
			}
		})
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("upstream calls = %d, want 0", got)
	}
}

func TestCohereProvider_Embed_UpstreamNon2xxError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"message":"rate limited"}`)
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	_, err := p.Embed(context.Background(), core.EmbeddingRequest{
		Model: "embed-english-v3.0",
		Input: "hello",
	})
	if err == nil {
		t.Fatal("expected upstream error, got nil")
	}
	if !strings.Contains(err.Error(), "cohere embed API error (429): rate limited") {
		t.Errorf("error = %q, want cohere embed API error (429): rate limited", err.Error())
	}
}
