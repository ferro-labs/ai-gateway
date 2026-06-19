package anthropic

import (
	"context"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// TestCompleteStream_ReportsUsage verifies #145: Anthropic streaming usage is
// parsed (input tokens from message_start, output tokens from message_delta)
// and surfaced on the final chunk instead of reporting zero tokens.
func TestCompleteStream_ReportsUsage(t *testing.T) {
	stream := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_1","model":"claude","role":"assistant","usage":{"input_tokens":25,"output_tokens":1,"cache_read_input_tokens":4,"cache_creation_input_tokens":6}}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":15}}` + "\n\n"

	p := newTestProvider(t, stream)
	ch, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "claude-3-5-sonnet",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}

	var usage *core.Usage
	for chunk := range ch {
		if chunk.Error != nil {
			t.Fatalf("stream error: %v", chunk.Error)
		}
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
	}

	if usage == nil {
		t.Fatal("no usage reported on stream")
	}
	if usage.PromptTokens != 25 {
		t.Errorf("PromptTokens = %d, want 25", usage.PromptTokens)
	}
	if usage.CompletionTokens != 15 {
		t.Errorf("CompletionTokens = %d, want 15", usage.CompletionTokens)
	}
	if usage.TotalTokens != 40 {
		t.Errorf("TotalTokens = %d, want 40", usage.TotalTokens)
	}
	if usage.CacheReadTokens != 4 || usage.CacheWriteTokens != 6 {
		t.Errorf("cache tokens = %d/%d, want 4/6", usage.CacheReadTokens, usage.CacheWriteTokens)
	}
}
