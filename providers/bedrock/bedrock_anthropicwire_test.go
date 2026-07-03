package bedrock

import (
	"context"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

func floatPtr(v float64) *float64 { return &v }

// TestBedrockProvider_CompleteStreamAnthropic_ReportsUsage verifies the shared
// anthropicwire stream decoder gives the Bedrock path the token usage it
// previously dropped: prompt tokens from message_start and completion tokens
// from message_delta are surfaced on the final chunk.
func TestBedrockProvider_CompleteStreamAnthropic_ReportsUsage(t *testing.T) {
	fake := &fakeBedrockRuntimeClient{
		streamResponses: [][]byte{
			[]byte(`{"type":"message_start","message":{"id":"msg_1","model":"claude-3-5-sonnet-20241022","usage":{"input_tokens":18}}}`),
			[]byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`),
			[]byte(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":6}}`),
		},
	}
	p := &Provider{name: Name, client: fake}

	ch, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "anthropic.claude-3-5-sonnet-20241022-v2:0",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("CompleteStream() error: %v", err)
	}

	var usage *core.Usage
	for c := range ch {
		if c.Error != nil {
			t.Fatalf("stream error: %v", c.Error)
		}
		if c.Usage != nil {
			usage = c.Usage
		}
	}
	if usage == nil {
		t.Fatal("bedrock stream reported no usage (message_start/message_delta usage dropped)")
	}
	if usage.PromptTokens != 18 || usage.CompletionTokens != 6 || usage.TotalTokens != 24 {
		t.Fatalf("usage = %d/%d/%d, want 18/6/24",
			usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens)
	}
}

// TestBedrockProvider_CompleteAnthropic_ClampsTemperature verifies the shared
// temperature clamp applies on the Bedrock Anthropic path too.
func TestBedrockProvider_CompleteAnthropic_ClampsTemperature(t *testing.T) {
	fake := &fakeBedrockRuntimeClient{
		responses: [][]byte{
			[]byte(`{"id":"msg_1","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`),
		},
	}
	p := &Provider{name: Name, client: fake}

	_, err := p.Complete(context.Background(), core.Request{
		Model:       "anthropic.claude-3-5-sonnet-20241022-v2:0",
		Messages:    []core.Message{{Role: core.RoleUser, Content: "hi"}},
		Temperature: floatPtr(1.8),
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}

	var body struct {
		Temperature *float64 `json:"temperature"`
	}
	mustUnmarshalBody(t, fake.invokeCalls[0].Body, &body)
	if body.Temperature == nil || *body.Temperature != 1.0 {
		t.Fatalf("temperature = %v, want clamped to 1.0", body.Temperature)
	}
}

// TestBedrockProvider_CompleteAnthropic_CapturesCacheTokens verifies prompt-cache
// token accounting is surfaced now that the Bedrock path shares the Anthropic
// response type.
func TestBedrockProvider_CompleteAnthropic_CapturesCacheTokens(t *testing.T) {
	fake := &fakeBedrockRuntimeClient{
		responses: [][]byte{
			[]byte(`{"id":"msg_1","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":4,"cache_read_input_tokens":3,"cache_creation_input_tokens":5}}`),
		},
	}
	p := &Provider{name: Name, client: fake}

	resp, err := p.Complete(context.Background(), core.Request{
		Model:    "anthropic.claude-3-5-sonnet-20241022-v2:0",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if resp.Usage.CacheReadTokens != 3 || resp.Usage.CacheWriteTokens != 5 {
		t.Fatalf("cache tokens = %d/%d, want 3/5", resp.Usage.CacheReadTokens, resp.Usage.CacheWriteTokens)
	}
}
