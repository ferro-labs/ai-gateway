package bedrock

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/ferro-labs/ai-gateway/providers/core"
	"github.com/ferro-labs/ai-gateway/providers/internal/anthropicwire"
)

type bedrockAnthropicRequest struct {
	AnthropicVersion string                  `json:"anthropic_version"`
	MaxTokens        int                     `json:"max_tokens"`
	Messages         []anthropicwire.Message `json:"messages"`
	Tools            []anthropicwire.Tool    `json:"tools,omitempty"`
	ToolChoice       any                     `json:"tool_choice,omitempty"`
	Temperature      *float64                `json:"temperature,omitempty"`
	TopP             *float64                `json:"top_p,omitempty"`
	StopSequences    []string                `json:"stop_sequences,omitempty"`
	System           string                  `json:"system,omitempty"`
}

// bedrockAnthropicDefaultMaxTokens is the max_tokens applied when a request does
// not specify one.
const bedrockAnthropicDefaultMaxTokens = 1024

// buildBedrockAnthropicRequest translates an OpenAI-shaped request into the
// Bedrock Anthropic invocation body, applying the default max_tokens when unset.
func buildBedrockAnthropicRequest(ctx context.Context, req core.Request) (bedrockAnthropicRequest, error) {
	maxTokens := bedrockAnthropicDefaultMaxTokens
	if req.MaxTokens != nil {
		maxTokens = *req.MaxTokens
	}

	// bedrockAnthropicContent can reject unsupported content (e.g. remote image
	// URLs); capture the first such error via the callback since
	// anthropicwire.BuildMessages' content callback returns only a value.
	var contentErr error
	messages, system := anthropicwire.BuildMessages(req, func(msg core.Message) any {
		blocks, err := bedrockAnthropicContent(msg)
		if err != nil && contentErr == nil {
			contentErr = err
		}
		return blocks
	})
	if contentErr != nil {
		return bedrockAnthropicRequest{}, contentErr
	}

	// Note: the native Anthropic provider maps the OpenAI "user" field to
	// metadata.user_id, but AWS Bedrock's InvokeModel Anthropic schema does not
	// document a metadata field, so it is intentionally not forwarded here.
	return bedrockAnthropicRequest{
		AnthropicVersion: "bedrock-2023-05-31",
		MaxTokens:        maxTokens,
		Messages:         messages,
		Tools:            anthropicwire.MapTools(req.Tools),
		ToolChoice:       anthropicwire.MapToolChoice(req.ToolChoice, req.Tools),
		Temperature:      anthropicwire.ClampTemperature(ctx, Name, req.Model, req.Temperature),
		TopP:             req.TopP,
		StopSequences:    req.Stop,
		System:           system,
	}, nil
}

// bedrockAnthropicContent renders a non-system message's content for Bedrock's
// Anthropic Messages API. Plain text turns stay a JSON string (the common
// path); multimodal turns (ContentParts, including image_url parts) and
// assistant tool calls become an array of content blocks. Bedrock Claude
// models accept the same content-block schema as the native Anthropic API, so
// this mirrors providers/anthropic's buildContent.
func bedrockAnthropicContent(msg core.Message) (any, error) {
	var blocks []anthropicwire.Block

	if len(msg.ContentParts) > 0 {
		for _, part := range msg.ContentParts {
			switch part.Type {
			case core.ContentTypeText:
				blocks = append(blocks, anthropicwire.Block{Type: "text", Text: part.Text})
			case "image_url":
				if part.ImageURL != nil {
					block, err := bedrockAnthropicImageBlock(part.ImageURL.URL)
					if err != nil {
						return nil, err
					}
					blocks = append(blocks, block)
				}
			}
		}
	} else if msg.Content != "" {
		blocks = append(blocks, anthropicwire.Block{Type: "text", Text: msg.Content})
	}

	for _, tc := range msg.ToolCalls {
		input := json.RawMessage(tc.Function.Arguments)
		if len(input) == 0 || !json.Valid(input) {
			input = json.RawMessage(`{}`)
		}
		blocks = append(blocks, anthropicwire.Block{
			Type:  "tool_use",
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Input: input,
		})
	}

	// Plain single-text turn: keep the lightweight string form so the common
	// path is byte-for-byte unchanged.
	if len(msg.ContentParts) == 0 && len(msg.ToolCalls) == 0 {
		return msg.Content, nil
	}
	return blocks, nil
}

// bedrockAnthropicImageBlock maps an OpenAI image_url (a base64 data URI) to a
// Bedrock Anthropic image content block. Bedrock's Anthropic models accept only
// base64-encoded images and do not fetch remote URLs (unlike the native
// Anthropic API), so a non-data-URI image is rejected with a clear error rather
// than emitting a "url" source block the Bedrock API would reject.
func bedrockAnthropicImageBlock(url string) (anthropicwire.Block, error) {
	mediaType, data, ok := anthropicwire.ParseDataURI(url)
	if !ok {
		return anthropicwire.Block{}, fmt.Errorf("bedrock anthropic: image inputs must be base64 data URIs; remote image URLs are not supported")
	}
	return anthropicwire.Block{
		Type: "image",
		Source: &anthropicwire.ImageSource{
			Type:      "base64",
			MediaType: mediaType,
			Data:      data,
		},
	}, nil
}

func (p *Provider) completeAnthropic(ctx context.Context, req core.Request) (*core.Response, error) {
	anthropicReq, err := buildBedrockAnthropicRequest(ctx, req)
	if err != nil {
		return nil, err
	}

	body, err := core.MarshalJSON(anthropicReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	output, err := p.client.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
		ModelId:     aws.String(req.Model),
		ContentType: aws.String("application/json"),
		Body:        body,
	})
	if err != nil {
		return nil, fmt.Errorf("bedrock invoke failed: %w", err)
	}

	var anthropicResp anthropicwire.Response
	if err := json.Unmarshal(output.Body, &anthropicResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	text, toolCalls := anthropicwire.DecodeContent(anthropicResp.Content)

	return &core.Response{
		ID:       anthropicResp.ID,
		Model:    req.Model,
		Provider: p.name,
		Choices: []core.Choice{{
			Index:        0,
			Message:      core.Message{Role: core.RoleAssistant, Content: text, ToolCalls: toolCalls},
			FinishReason: core.NormalizeFinishReason(anthropicResp.StopReason),
		}},
		Usage: core.Usage{
			PromptTokens:     anthropicResp.Usage.InputTokens,
			CompletionTokens: anthropicResp.Usage.OutputTokens,
			TotalTokens:      anthropicResp.Usage.InputTokens + anthropicResp.Usage.OutputTokens,
			CacheReadTokens:  anthropicResp.Usage.CacheReadInputTokens,
			CacheWriteTokens: anthropicResp.Usage.CacheCreationInputTokens,
		},
	}, nil
}
