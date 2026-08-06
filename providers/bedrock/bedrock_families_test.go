package bedrock

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
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

// The conversation every InvokeModel-family body assertion below is built from.
// It carries a system turn, an assistant turn and two user turns, so a body that
// loses roles, loses ordering, or keeps only one turn cannot match.
func probeConversation() []core.Message {
	return []core.Message{
		{Role: core.RoleSystem, Content: "You are terse."},
		{Role: core.RoleUser, Content: "first"},
		{Role: core.RoleAssistant, Content: "reply"},
		{Role: core.RoleUser, Content: "second"},
	}
}

const (
	titanFakeResponse = `{"inputTextTokenCount":4,"results":[{"tokenCount":6,"outputText":"ok","completionReason":"FINISH"}]}`
	llamaFakeResponse = `{"generation":"ok","prompt_token_count":3,"generation_token_count":2,"stop_reason":"stop"}`
	novaFakeResponse  = `{"output":{"message":{"role":"assistant","content":[{"text":"ok"}]}},"stopReason":"end_turn","usage":{"inputTokens":1,"outputTokens":1}}`
)

// TestBedrockProvider_Complete_TitanPromptCarriesEveryTurn asserts the inputText
// Titan actually receives. It used to be the messages' bare content joined by
// newlines: every role was lost and there was no completion cue, so the model was
// handed an undifferentiated wall of text and tended to continue the caller's
// last turn rather than answer it.
func TestBedrockProvider_Complete_TitanPromptCarriesEveryTurn(t *testing.T) {
	cases := []struct {
		name     string
		messages []core.Message
		want     string
	}{
		{
			name:     "single message",
			messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
			want:     "user: hi\nassistant: ",
		},
		{
			name:     "conversation",
			messages: probeConversation(),
			want:     "system: You are terse.\nuser: first\nassistant: reply\nuser: second\nassistant: ",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeBedrockRuntimeClient{responses: [][]byte{[]byte(titanFakeResponse)}}
			p := &Provider{name: Name, client: fake}

			if _, err := p.Complete(context.Background(), core.Request{
				Model:    "amazon.titan-text-express-v1",
				Messages: tc.messages,
			}); err != nil {
				t.Fatalf("Complete() error: %v", err)
			}

			var body bedrockTitanRequest
			mustUnmarshalBody(t, fake.invokeCalls[0].Body, &body)
			if body.InputText != tc.want {
				t.Errorf("inputText = %q, want %q", body.InputText, tc.want)
			}
		})
	}
}

// TestBedrockProvider_Complete_LlamaPromptCarriesEveryTurn asserts the prompt
// Llama actually receives. The format is the tokenizer-level template Llama 3 was
// instruction-tuned on and is deliberately not the generic join; what is shared
// is only the guard on content a prompt string cannot carry.
func TestBedrockProvider_Complete_LlamaPromptCarriesEveryTurn(t *testing.T) {
	const turn = "<|start_header_id|>%s<|end_header_id|>\n\n%s<|eot_id|>\n"
	cases := []struct {
		name     string
		messages []core.Message
		want     string
	}{
		{
			name:     "single message",
			messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
			want: "<|begin_of_text|>" +
				fmt.Sprintf(turn, "user", "hi") +
				"<|start_header_id|>assistant<|end_header_id|>\n\n",
		},
		{
			name:     "conversation",
			messages: probeConversation(),
			want: "<|begin_of_text|>" +
				fmt.Sprintf(turn, "system", "You are terse.") +
				fmt.Sprintf(turn, "user", "first") +
				fmt.Sprintf(turn, "assistant", "reply") +
				fmt.Sprintf(turn, "user", "second") +
				"<|start_header_id|>assistant<|end_header_id|>\n\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeBedrockRuntimeClient{responses: [][]byte{[]byte(llamaFakeResponse)}}
			p := &Provider{name: Name, client: fake}

			if _, err := p.Complete(context.Background(), core.Request{
				Model:    "meta.llama3-1-8b-instruct-v1:0",
				Messages: tc.messages,
			}); err != nil {
				t.Fatalf("Complete() error: %v", err)
			}

			var body bedrockLlamaRequest
			mustUnmarshalBody(t, fake.invokeCalls[0].Body, &body)
			if body.Prompt != tc.want {
				t.Errorf("prompt = %q, want %q", body.Prompt, tc.want)
			}
		})
	}
}

// TestBedrockProvider_Complete_NovaCarriesEveryTurn asserts Nova keeps its native
// shape: a separate system channel and one message per turn, in order. Nova is
// not a single-prompt family and must never be flattened into one — it shares
// only the guard on inexpressible content.
func TestBedrockProvider_Complete_NovaCarriesEveryTurn(t *testing.T) {
	fake := &fakeBedrockRuntimeClient{responses: [][]byte{[]byte(novaFakeResponse)}}
	p := &Provider{name: Name, client: fake}

	if _, err := p.Complete(context.Background(), core.Request{
		Model:    "amazon.nova-pro-v1:0",
		Messages: probeConversation(),
	}); err != nil {
		t.Fatalf("Complete() error: %v", err)
	}

	var body bedrockNovaRequest
	mustUnmarshalBody(t, fake.invokeCalls[0].Body, &body)

	if len(body.System) != 1 || body.System[0].Text != "You are terse." {
		t.Errorf("system = %#v, want the system turn on its own channel", body.System)
	}
	want := []bedrockNovaMessage{
		{Role: core.RoleUser, Content: []bedrockNovaTextBlock{{Text: "first"}}},
		{Role: core.RoleAssistant, Content: []bedrockNovaTextBlock{{Text: "reply"}}},
		{Role: core.RoleUser, Content: []bedrockNovaTextBlock{{Text: "second"}}},
	}
	if !reflect.DeepEqual(body.Messages, want) {
		t.Errorf("messages = %#v, want %#v", body.Messages, want)
	}
}

// TestBedrockProvider_Complete_InvokeModelFamiliesRefuseImageParts pins the
// replacement for a silent drop: an image part reached none of these families —
// the InvokeModel bodies have no field for it — and the caller got a 200 over
// content the model never saw. It is a 400 now, raised before the upstream call.
func TestBedrockProvider_Complete_InvokeModelFamiliesRefuseImageParts(t *testing.T) {
	cases := []struct {
		name     string
		model    string
		response string
	}{
		{name: "titan", model: "amazon.titan-text-express-v1", response: titanFakeResponse},
		{name: "llama", model: "meta.llama3-1-8b-instruct-v1:0", response: llamaFakeResponse},
		{name: "nova", model: "amazon.nova-pro-v1:0", response: novaFakeResponse},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeBedrockRuntimeClient{responses: [][]byte{[]byte(tc.response)}}
			p := &Provider{name: Name, client: fake}

			_, err := p.Complete(context.Background(), core.Request{
				Model: tc.model,
				Messages: []core.Message{{
					Role:    core.RoleUser,
					Content: "describe",
					ContentParts: []core.ContentPart{
						{Type: core.ContentTypeText, Text: "describe"},
						{Type: "image_url", ImageURL: &core.ImageURLPart{URL: "data:image/png;base64,QUJD"}},
					},
				}},
			})
			if err == nil {
				t.Fatal("Complete() succeeded; an image part this family cannot carry must be refused, not dropped")
			}
			if got := core.ParseStatusCode(err); got != http.StatusBadRequest {
				t.Errorf("ParseStatusCode = %d, want %d — the caller can fix this and no retry can", got, http.StatusBadRequest)
			}
			if len(fake.invokeCalls) != 0 {
				t.Errorf("InvokeModel calls = %d, want 0 — the refusal must precede the upstream call", len(fake.invokeCalls))
			}
		})
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

// TestBedrockProvider_Complete_DeveloperRoleBecomesSystem verifies OpenAI's
// "developer" role reaches each family's system channel; forwarded verbatim it
// is a role Bedrock rejects.
func TestBedrockProvider_Complete_DeveloperRoleBecomesSystem(t *testing.T) {
	t.Run("anthropic", func(t *testing.T) {
		fake := &fakeBedrockRuntimeClient{
			responses: [][]byte{
				[]byte(`{"id":"msg-1","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`),
			},
		}
		p := &Provider{name: Name, client: fake}

		if _, err := p.Complete(context.Background(), core.Request{
			Model: "anthropic.claude-3-5-sonnet-20241022-v2:0",
			Messages: []core.Message{
				{Role: core.RoleDeveloper, Content: "be concise"},
				{Role: core.RoleUser, Content: "hello"},
			},
		}); err != nil {
			t.Fatalf("Complete() error: %v", err)
		}

		var body bedrockAnthropicRequest
		mustUnmarshalBody(t, fake.invokeCalls[0].Body, &body)
		if body.System != "be concise" {
			t.Errorf("system = %q, want the developer turn", body.System)
		}
		if len(body.Messages) != 1 || body.Messages[0].Role != core.RoleUser {
			t.Errorf("messages = %#v, want the user turn only", body.Messages)
		}
	})

	t.Run("nova", func(t *testing.T) {
		fake := &fakeBedrockRuntimeClient{
			responses: [][]byte{
				[]byte(`{"output":{"message":{"role":"assistant","content":[{"text":"hi"}]}},"stopReason":"end_turn","usage":{"inputTokens":1,"outputTokens":1,"totalTokens":2}}`),
			},
		}
		p := &Provider{name: Name, client: fake}

		if _, err := p.Complete(context.Background(), core.Request{
			Model: "amazon.nova-pro-v1:0",
			Messages: []core.Message{
				{Role: core.RoleDeveloper, Content: "be concise"},
				{Role: core.RoleUser, Content: "hello"},
			},
		}); err != nil {
			t.Fatalf("Complete() error: %v", err)
		}

		var body bedrockNovaRequest
		mustUnmarshalBody(t, fake.invokeCalls[0].Body, &body)
		if len(body.System) != 1 || body.System[0].Text != "be concise" {
			t.Errorf("system = %#v, want the developer turn", body.System)
		}
		if len(body.Messages) != 1 || body.Messages[0].Role != core.RoleUser {
			t.Errorf("messages = %#v, want the user turn only", body.Messages)
		}
	})
}

// TestBedrockProvider_Complete_AnthropicUnrenderablePartsKeepText verifies a turn
// whose content parts are all kinds Bedrock's Anthropic schema cannot express
// falls back to the collapsed text rather than a null content the API rejects.
func TestBedrockProvider_Complete_AnthropicUnrenderablePartsKeepText(t *testing.T) {
	fake := &fakeBedrockRuntimeClient{
		responses: [][]byte{
			[]byte(`{"id":"msg-1","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`),
		},
	}
	p := &Provider{name: Name, client: fake}

	if _, err := p.Complete(context.Background(), core.Request{
		Model: "anthropic.claude-3-5-sonnet-20241022-v2:0",
		Messages: []core.Message{{
			Role:         core.RoleUser,
			Content:      "transcribe this",
			ContentParts: []core.ContentPart{{Type: "input_audio"}},
		}},
	}); err != nil {
		t.Fatalf("Complete() error: %v", err)
	}

	if got := string(fake.invokeCalls[0].Body); strings.Contains(got, `"content":null`) {
		t.Fatalf("request body = %s, want the collapsed text instead of a null content", got)
	}
	var body bedrockAnthropicRequest
	mustUnmarshalBody(t, fake.invokeCalls[0].Body, &body)
	if len(body.Messages) != 1 || body.Messages[0].Content != "transcribe this" {
		t.Errorf("messages = %#v, want the collapsed text content", body.Messages)
	}
}
