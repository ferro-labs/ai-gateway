package streamwrap

import (
	"context"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// toolCallDelta builds one streaming tool-call fragment in the shape every
// provider emits it: keyed by index, identity fields only on the first.
func toolCallDelta(index int, id, name, args string) providers.StreamChunk {
	return providers.StreamChunk{Choices: []providers.StreamChoice{{
		Delta: providers.MessageDelta{ToolCalls: []providers.ToolCall{{
			Index:    core.Ptr(index),
			ID:       id,
			Type:     "function",
			Function: providers.FunctionCall{Name: name, Arguments: args},
		}}},
	}}}
}

// completedResponse runs chunks through Meter and returns the response the
// after_request stage is handed.
func completedResponse(t *testing.T, chunks ...providers.StreamChunk) providers.Response {
	t.Helper()
	var got providers.Response
	out := Meter(context.Background(), feed(chunks...), time.Now(), MeterMeta{
		Provider:    "openai",
		Model:       "gpt-4o",
		MetricModel: "gpt-4o",
		Catalog:     models.Catalog{},
		CompletionFn: func(_ context.Context, resp *providers.Response, _ Measurements) error {
			got = *resp
			return nil
		},
	})
	for range out { //nolint:revive // empty-block: intentionally draining the stream to completion
	}
	return got
}

// TestMeter_AssemblesToolCallsByIndex pins the OpenAI streaming contract for the
// assembled response: fragments are keyed by index, so two parallel tool calls
// streamed over five frames must arrive as two whole calls. Accumulating the
// fragments verbatim hands the cache plugin, the request log, and cost
// accounting a response the model never produced.
func TestMeter_AssemblesToolCallsByIndex(t *testing.T) {
	resp := completedResponse(t,
		toolCallDelta(0, "call_a", "get_weather", ""),
		toolCallDelta(1, "call_b", "get_time", ""),
		toolCallDelta(0, "", "", `{"city":`),
		toolCallDelta(1, "", "", `{"tz":"UTC"}`),
		toolCallDelta(0, "", "", `"Paris"}`),
	)

	if len(resp.Choices) != 1 {
		t.Fatalf("choices = %d, want 1", len(resp.Choices))
	}
	calls := resp.Choices[0].Message.ToolCalls
	if len(calls) != 2 {
		t.Fatalf("tool calls = %d, want 2 whole calls; got %+v", len(calls), calls)
	}

	want := []struct{ id, name, args string }{
		{"call_a", "get_weather", `{"city":"Paris"}`},
		{"call_b", "get_time", `{"tz":"UTC"}`},
	}
	for i, w := range want {
		if calls[i].ID != w.id || calls[i].Function.Name != w.name || calls[i].Function.Arguments != w.args {
			t.Errorf("tool call %d = %+v, want id %q name %q arguments %q",
				i, calls[i], w.id, w.name, w.args)
		}
		if calls[i].Type != "function" {
			t.Errorf("tool call %d type = %q, want function", i, calls[i].Type)
		}
	}
}

// TestMeter_KeepsUnindexedToolCallsSeparate covers the providers that emit each
// tool call whole and unkeyed: with no index to merge on, every fragment stands
// as its own call.
func TestMeter_KeepsUnindexedToolCallsSeparate(t *testing.T) {
	unindexed := func(id, name string) providers.StreamChunk {
		return providers.StreamChunk{Choices: []providers.StreamChoice{{
			Delta: providers.MessageDelta{ToolCalls: []providers.ToolCall{{
				ID:       id,
				Type:     "function",
				Function: providers.FunctionCall{Name: name, Arguments: "{}"},
			}}},
		}}}
	}

	resp := completedResponse(t, unindexed("call_a", "one"), unindexed("call_b", "two"))

	calls := resp.Choices[0].Message.ToolCalls
	if len(calls) != 2 {
		t.Fatalf("tool calls = %d, want 2; got %+v", len(calls), calls)
	}
	if calls[0].ID != "call_a" || calls[1].ID != "call_b" {
		t.Errorf("tool call ids = %q, %q, want call_a, call_b", calls[0].ID, calls[1].ID)
	}
}
