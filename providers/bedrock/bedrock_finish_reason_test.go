package bedrock

import (
	"context"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// TestComplete_NormalizesFinishReason verifies #142: native Bedrock stop
// reasons across the Claude, Titan, and Llama families are mapped to the
// OpenAI-canonical finish_reason vocabulary.
func TestComplete_NormalizesFinishReason(t *testing.T) {
	cases := []struct {
		name  string
		model string
		body  string
		want  string
	}{
		{
			name:  "claude tool_use -> tool_calls",
			model: "anthropic.claude-3-5-sonnet-20240620-v1:0",
			body:  `{"id":"x","content":[{"type":"text","text":"hi"}],"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1}}`,
			want:  "tool_calls",
		},
		{
			name:  "claude max_tokens -> length",
			model: "anthropic.claude-3-5-sonnet-20240620-v1:0",
			body:  `{"id":"x","content":[{"type":"text","text":"hi"}],"stop_reason":"max_tokens","usage":{"input_tokens":1,"output_tokens":1}}`,
			want:  "length",
		},
		{
			name:  "titan LENGTH -> length",
			model: "amazon.titan-text-express-v1",
			body:  `{"inputTextTokenCount":1,"results":[{"tokenCount":1,"outputText":"hi","completionReason":"LENGTH"}]}`,
			want:  "length",
		},
		{
			name:  "titan FINISH -> stop",
			model: "amazon.titan-text-express-v1",
			body:  `{"inputTextTokenCount":1,"results":[{"tokenCount":1,"outputText":"hi","completionReason":"FINISH"}]}`,
			want:  "stop",
		},
		{
			name:  "llama length -> length",
			model: "meta.llama3-8b-instruct-v1:0",
			body:  `{"generation":"hi","prompt_token_count":1,"generation_token_count":1,"stop_reason":"length"}`,
			want:  "length",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeBedrockRuntimeClient{responses: [][]byte{[]byte(tc.body)}}
			p := &Provider{name: Name, client: fake}

			resp, err := p.Complete(context.Background(), core.Request{
				Model:    tc.model,
				Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
			})
			if err != nil {
				t.Fatalf("Complete() error: %v", err)
			}
			if got := resp.Choices[0].FinishReason; got != tc.want {
				t.Errorf("finish_reason = %q, want %q", got, tc.want)
			}
		})
	}
}
