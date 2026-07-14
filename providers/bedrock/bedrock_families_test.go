package bedrock

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// TestBedrockProvider_Complete_TitanSetsTotalTokensAndID verifies Titan responses
// report TotalTokens and carry a synthesized response ID.
func TestBedrockProvider_Complete_TitanSetsTotalTokensAndID(t *testing.T) {
	fake := &fakeBedrockRuntimeClient{
		responses: [][]byte{
			[]byte(`{"inputTextTokenCount":4,"results":[{"tokenCount":6,"outputText":"hi","completionReason":"FINISH"}]}`),
		},
	}
	p := &Provider{name: Name, client: fake}

	resp, err := p.Complete(context.Background(), core.Request{
		Model:    "amazon.titan-text-express-v1",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if resp.Usage.TotalTokens != 10 {
		t.Errorf("TotalTokens = %d, want 10 (prompt 4 + completion 6)", resp.Usage.TotalTokens)
	}
	if !strings.HasPrefix(resp.ID, "bedrock-") {
		t.Errorf("ID = %q, want a synthesized bedrock- id", resp.ID)
	}
}

// TestBedrockProvider_Complete_LlamaSynthesizesID verifies the Llama family also
// gets a synthesized response ID.
func TestBedrockProvider_Complete_LlamaSynthesizesID(t *testing.T) {
	fake := &fakeBedrockRuntimeClient{
		responses: [][]byte{
			[]byte(`{"generation":"hi","prompt_token_count":3,"generation_token_count":2,"stop_reason":"stop"}`),
		},
	}
	p := &Provider{name: Name, client: fake}

	resp, err := p.Complete(context.Background(), core.Request{
		Model:    "meta.llama3-1-8b-instruct-v1:0",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if !strings.HasPrefix(resp.ID, "bedrock-") {
		t.Errorf("ID = %q, want a synthesized bedrock- id", resp.ID)
	}
}

// TestBedrockProvider_Complete_NovaDropsImagePartsGracefully verifies a Nova
// request carrying an image part still returns its text answer (the image is
// warn-dropped, not fatal).
func TestBedrockProvider_Complete_NovaDropsImagePartsGracefully(t *testing.T) {
	fake := &fakeBedrockRuntimeClient{
		responses: [][]byte{
			[]byte(`{"output":{"message":{"role":"assistant","content":[{"text":"ok"}]}},"stopReason":"end_turn","usage":{"inputTokens":1,"outputTokens":1}}`),
		},
	}
	p := &Provider{name: Name, client: fake}

	resp, err := p.Complete(context.Background(), core.Request{
		Model: "amazon.nova-pro-v1:0",
		Messages: []core.Message{{
			Role: core.RoleUser,
			ContentParts: []core.ContentPart{
				{Type: "text", Text: "describe"},
				{Type: "image_url", ImageURL: &core.ImageURLPart{URL: "data:image/png;base64,QUJD"}},
			},
		}},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if resp.Choices[0].Message.Content != "ok" {
		t.Errorf("content = %q, want ok", resp.Choices[0].Message.Content)
	}
	if !strings.HasPrefix(resp.ID, "bedrock-") {
		t.Errorf("ID = %q, want a synthesized bedrock- id", resp.ID)
	}
}

// TestBedrockProvider_Complete_UnknownModelReportsModelErrorNotParamError
// verifies an unrecognized/mismatched model (an embeddings-only model routed
// to chat completion) reports the real "unsupported model prefix" error
// rather than a misleading "does not support request parameter(s)" error when
// reject mode is active and an optional param is set. Family resolution must
// happen before parameter enforcement, since bedrockSupportedParams' default
// case (nil) is indistinguishable from "supports nothing" otherwise.
func TestBedrockProvider_Complete_UnknownModelReportsModelErrorNotParamError(t *testing.T) {
	p := &Provider{name: Name, client: &fakeBedrockRuntimeClient{}}
	ctx := core.WithUnsupportedParamMode(context.Background(), core.UnsupportedParamReject)
	temp := 0.5

	_, err := p.Complete(ctx, core.Request{
		Model:       "cohere.embed-english-v3",
		Messages:    []core.Message{{Role: core.RoleUser, Content: "hi"}},
		Temperature: &temp,
	})
	if err == nil {
		t.Fatal("Complete() error = nil, want the unsupported-model-prefix error")
	}
	var paramErr *core.UnsupportedParamError
	if errors.As(err, &paramErr) {
		t.Fatalf("Complete() error = %v (*core.UnsupportedParamError), want the model-prefix error instead", err)
	}
	if !strings.Contains(err.Error(), "unsupported Bedrock model prefix") {
		t.Errorf("Complete() error = %q, want it to name the unsupported model prefix", err.Error())
	}
}
