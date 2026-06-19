package openaicompat

import (
	"encoding/json"
	"io"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

func ptrF(f float64) *float64 { return &f }
func ptrI(i int) *int         { return &i }
func ptrI64(i int64) *int64   { return &i }

// TestBuildBody_ForwardsEverySamplingParam is the regression guard for #140:
// the shared builder must forward every OpenAI-shaped sampling/output field, not
// the old {model,messages,temperature,max_tokens} subset.
func TestBuildBody_ForwardsEverySamplingParam(t *testing.T) {
	req := core.Request{
		Model: "some-model",
		Messages: []core.Message{
			{Role: "user", Content: "hi"},
			{
				Role: "assistant",
				ToolCalls: []core.ToolCall{{
					ID:       "call_1",
					Type:     "function",
					Function: core.FunctionCall{Name: "lookup", Arguments: `{"city":"SF"}`},
				}},
			},
			{Role: "tool", ToolCallID: "call_1", Content: `{"temp":"72F"}`},
		},
		Temperature:         ptrF(0.7),
		TopP:                ptrF(0.9),
		N:                   ptrI(2),
		Seed:                ptrI64(42),
		MaxTokens:           ptrI(100),
		MaxCompletionTokens: ptrI(120),
		PresencePenalty:     ptrF(0.5),
		FrequencyPenalty:    ptrF(0.25),
		Stop:                []string{"END"},
		Tools: []core.Tool{{
			Type: "function",
			Function: core.Function{
				Name:        "lookup",
				Description: "Look up weather",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`),
			},
		}},
		ToolChoice:     map[string]any{"type": "function", "function": map[string]any{"name": "lookup"}},
		ResponseFormat: &core.ResponseFormat{Type: "json_object"},
		TopLogProbs:    ptrI(3),
		LogProbs:       true,
		User:           "user-123",
		LogitBias:      map[string]float64{"50256": -100},
	}

	body, _, release, err := BuildBody(req, false)
	if err != nil {
		t.Fatalf("BuildBody returned error: %v", err)
	}
	defer release()

	raw, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	var got map[string]json.RawMessage
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal body: %v\nbody=%s", err, raw)
	}

	mustHave := []string{
		"model", "messages", "temperature", "top_p", "n", "seed",
		"max_tokens", "max_completion_tokens", "presence_penalty",
		"frequency_penalty", "stop", "tools", "tool_choice", "response_format", "logprobs",
		"top_logprobs", "user", "logit_bias",
	}
	for _, k := range mustHave {
		if _, ok := got[k]; !ok {
			t.Errorf("field %q was dropped by BuildBody; body=%s", k, raw)
		}
	}

	var decoded struct {
		Messages []core.Message `json:"messages"`
		Tools    []core.Tool    `json:"tools"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal typed body: %v", err)
	}
	if len(decoded.Tools) != 1 || decoded.Tools[0].Function.Name != "lookup" {
		t.Fatalf("tools = %#v, want lookup tool", decoded.Tools)
	}
	if len(decoded.Messages) != 3 {
		t.Fatalf("messages len = %d, want 3", len(decoded.Messages))
	}
	if len(decoded.Messages[1].ToolCalls) != 1 || decoded.Messages[1].ToolCalls[0].Function.Name != "lookup" {
		t.Fatalf("assistant tool_calls = %#v, want lookup call", decoded.Messages[1].ToolCalls)
	}
	if decoded.Messages[2].ToolCallID != "call_1" {
		t.Fatalf("tool_call_id = %q, want call_1", decoded.Messages[2].ToolCallID)
	}
}

func TestBuildBody_StreamFlag(t *testing.T) {
	req := core.Request{Model: "m", Messages: []core.Message{{Role: "user", Content: "x"}}}

	// stream=false → omitempty drops the field (matches prior per-provider behaviour).
	body, _, release, err := BuildBody(req, false)
	if err != nil {
		t.Fatalf("BuildBody: %v", err)
	}
	raw, _ := io.ReadAll(body)
	release()
	var off map[string]json.RawMessage
	_ = json.Unmarshal(raw, &off)
	if _, ok := off["stream"]; ok {
		t.Errorf("stream should be omitted when false; body=%s", raw)
	}

	// stream=true → field present and true.
	body2, _, release2, err := BuildBody(req, true)
	if err != nil {
		t.Fatalf("BuildBody: %v", err)
	}
	raw2, _ := io.ReadAll(body2)
	release2()
	var on struct {
		Stream bool `json:"stream"`
	}
	if err := json.Unmarshal(raw2, &on); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !on.Stream {
		t.Errorf("stream should be true; body=%s", raw2)
	}
}

func TestDecodeStreamChunk_ForwardsToolCallDeltas(t *testing.T) {
	chunk, err := DecodeStreamChunk([]byte(`{
		"id":"chatcmpl-1",
		"object":"chat.completion.chunk",
		"created":123,
		"model":"some-model",
		"choices":[{
			"index":0,
			"delta":{
				"role":"assistant",
				"tool_calls":[{
					"index":0,
					"id":"call_1",
					"type":"function",
					"function":{"name":"lookup","arguments":"{\"city\":\"SF\"}"}
				}]
			},
			"finish_reason":"tool_calls"
		}]
	}`))
	if err != nil {
		t.Fatalf("DecodeStreamChunk returned error: %v", err)
	}
	if chunk.ID != "chatcmpl-1" || chunk.Model != "some-model" {
		t.Fatalf("chunk id/model = %q/%q, want chatcmpl-1/some-model", chunk.ID, chunk.Model)
	}
	if len(chunk.Choices) != 1 {
		t.Fatalf("choices len = %d, want 1", len(chunk.Choices))
	}
	got := chunk.Choices[0].Delta.ToolCalls
	if len(got) != 1 {
		t.Fatalf("tool_calls len = %d, want 1", len(got))
	}
	if got[0].Index == nil || *got[0].Index != 0 {
		t.Fatalf("tool call index = %#v, want 0", got[0].Index)
	}
	if got[0].ID != "call_1" || got[0].Function.Name != "lookup" || got[0].Function.Arguments != `{"city":"SF"}` {
		t.Fatalf("tool call = %#v, want lookup call", got[0])
	}
	if chunk.Choices[0].FinishReason != core.FinishReasonToolCalls {
		t.Fatalf("finish_reason = %q, want %q", chunk.Choices[0].FinishReason, core.FinishReasonToolCalls)
	}
}
