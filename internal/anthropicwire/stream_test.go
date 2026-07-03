package anthropicwire

import (
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// drain feeds a sequence of raw events through a decoder and collects every
// emitted chunk plus the first terminal error.
func drain(d *StreamDecoder, events ...string) ([]core.StreamChunk, error) {
	var out []core.StreamChunk
	for _, e := range events {
		chunks, err := d.Event([]byte(e))
		out = append(out, chunks...)
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

func TestStreamDecoder_CapturesUsageAcrossStartAndDelta(t *testing.T) {
	d := NewStreamDecoder("anthropic", "")
	chunks, err := drain(d,
		`{"type":"message_start","message":{"id":"msg_1","model":"claude","role":"assistant","usage":{"input_tokens":25,"cache_read_input_tokens":4,"cache_creation_input_tokens":6}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":15}}`,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// text chunk carries the message_start id + model (no fallback given).
	if chunks[0].ID != "msg_1" || chunks[0].Model != "claude" {
		t.Fatalf("text chunk id/model = %q/%q, want msg_1/claude", chunks[0].ID, chunks[0].Model)
	}
	if chunks[0].Choices[0].Delta.Content != "hi" {
		t.Fatalf("text = %q, want hi", chunks[0].Choices[0].Delta.Content)
	}

	final := chunks[len(chunks)-1]
	if final.Choices[0].FinishReason != core.FinishReasonStop {
		t.Fatalf("finish = %q, want stop", final.Choices[0].FinishReason)
	}
	if final.Usage == nil {
		t.Fatal("final chunk has no usage")
	}
	if final.Usage.PromptTokens != 25 || final.Usage.CompletionTokens != 15 || final.Usage.TotalTokens != 40 {
		t.Fatalf("usage tokens = %d/%d/%d, want 25/15/40",
			final.Usage.PromptTokens, final.Usage.CompletionTokens, final.Usage.TotalTokens)
	}
	if final.Usage.CacheReadTokens != 4 || final.Usage.CacheWriteTokens != 6 {
		t.Fatalf("cache = %d/%d, want 4/6", final.Usage.CacheReadTokens, final.Usage.CacheWriteTokens)
	}
}

// TestStreamDecoder_FallbackModelAndUsage exercises the Bedrock-style path: no
// message_start model is emitted on chunks (the request model wins), and usage
// is still captured — the behaviour the Bedrock stream previously dropped.
func TestStreamDecoder_FallbackModelAndUsage(t *testing.T) {
	d := NewStreamDecoder("bedrock", "anthropic.claude-3-5-sonnet-20241022-v2:0")
	chunks, err := drain(d,
		`{"type":"message_start","message":{"id":"msg_9","model":"claude-3-5-sonnet-20241022","usage":{"input_tokens":11}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}`,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, c := range chunks {
		if c.Model != "anthropic.claude-3-5-sonnet-20241022-v2:0" {
			t.Fatalf("chunk model = %q, want request model (fallback)", c.Model)
		}
	}
	final := chunks[len(chunks)-1]
	if final.Usage == nil || final.Usage.PromptTokens != 11 || final.Usage.CompletionTokens != 7 {
		t.Fatalf("bedrock-path usage = %#v, want prompt 11 / completion 7", final.Usage)
	}
}

func TestStreamDecoder_ToolUseSequenceAndZeroArgTool(t *testing.T) {
	d := NewStreamDecoder("anthropic", "")
	// Two tool calls: the first receives arguments, the second is zero-argument
	// and must get a synthesised "{}" before the finish chunk.
	chunks, err := drain(d,
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"a"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"x\":1}"}}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_2","name":"b"}}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// start(a), args(a), start(b), zero-arg(b), finish = 5 chunks.
	if len(chunks) != 5 {
		t.Fatalf("chunks = %d, want 5: %#v", len(chunks), chunks)
	}
	if idx := chunks[0].Choices[0].Delta.ToolCalls[0].Index; idx == nil || *idx != 0 {
		t.Fatalf("first tool index = %v, want 0", idx)
	}
	if idx := chunks[2].Choices[0].Delta.ToolCalls[0].Index; idx == nil || *idx != 1 {
		t.Fatalf("second tool index = %v, want 1", idx)
	}
	zeroArg := chunks[3].Choices[0].Delta.ToolCalls[0]
	if zeroArg.Index == nil || *zeroArg.Index != 1 || zeroArg.Function.Arguments != "{}" {
		t.Fatalf("zero-arg tool chunk = %#v, want index 1 with {}", zeroArg)
	}
	if chunks[4].Choices[0].FinishReason != core.FinishReasonToolCalls {
		t.Fatalf("finish = %q, want tool_calls", chunks[4].Choices[0].FinishReason)
	}
}

func TestStreamDecoder_ErrorEventStopsStream(t *testing.T) {
	d := NewStreamDecoder("anthropic", "")
	chunks, err := d.Event([]byte(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`))
	if len(chunks) != 0 {
		t.Fatalf("error event emitted %d chunks, want 0", len(chunks))
	}
	if err == nil {
		t.Fatal("error event returned nil error")
	}
	if !strings.Contains(err.Error(), "overloaded_error") || !strings.Contains(err.Error(), "Overloaded") {
		t.Fatalf("error = %v, want type + message", err)
	}
	if !strings.HasPrefix(err.Error(), "anthropic stream error") {
		t.Fatalf("error = %v, want provider-labelled prefix", err)
	}
}
