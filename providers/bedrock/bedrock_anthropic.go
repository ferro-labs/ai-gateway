package bedrock

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/ferro-labs/ai-gateway/internal/anthropicwire"
	"github.com/ferro-labs/ai-gateway/providers/core"
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

type bedrockAnthropicResponse struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Role    string `json:"role"`
	Content []struct {
		Type  string          `json:"type"`
		Text  string          `json:"text"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// bedrockAnthropicDefaultMaxTokens is the max_tokens applied when a request does
// not specify one.
const bedrockAnthropicDefaultMaxTokens = 1024

// buildBedrockAnthropicRequest translates an OpenAI-shaped request into the
// Bedrock Anthropic invocation body, applying the default max_tokens when unset.
func buildBedrockAnthropicRequest(req core.Request) bedrockAnthropicRequest {
	maxTokens := bedrockAnthropicDefaultMaxTokens
	if req.MaxTokens != nil {
		maxTokens = *req.MaxTokens
	}

	messages, system := anthropicwire.BuildMessages(req, bedrockAnthropicContent)

	return bedrockAnthropicRequest{
		AnthropicVersion: "bedrock-2023-05-31",
		MaxTokens:        maxTokens,
		Messages:         messages,
		Tools:            anthropicwire.MapTools(req.Tools),
		ToolChoice:       anthropicwire.MapToolChoice(req.ToolChoice, req.Tools),
		Temperature:      req.Temperature,
		TopP:             req.TopP,
		StopSequences:    req.Stop,
		System:           system,
	}
}

// bedrockAnthropicContent renders a non-system message's content for Bedrock's
// Anthropic Messages API. Plain text turns stay a JSON string (the common
// path); multimodal turns (ContentParts, including image_url parts) and
// assistant tool calls become an array of content blocks. Bedrock Claude
// models accept the same content-block schema as the native Anthropic API, so
// this mirrors providers/anthropic's buildContent.
func bedrockAnthropicContent(msg core.Message) any {
	var blocks []anthropicwire.Block

	if len(msg.ContentParts) > 0 {
		for _, part := range msg.ContentParts {
			switch part.Type {
			case core.ContentTypeText:
				blocks = append(blocks, anthropicwire.Block{Type: "text", Text: part.Text})
			case "image_url":
				if part.ImageURL != nil {
					blocks = append(blocks, bedrockAnthropicImageBlock(part.ImageURL.URL))
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
		return msg.Content
	}
	return blocks
}

// bedrockAnthropicImageBlock maps an OpenAI image_url (data URI or remote URL)
// to a Bedrock Anthropic image content block.
func bedrockAnthropicImageBlock(url string) anthropicwire.Block {
	if mediaType, data, ok := bedrockParseDataURI(url); ok {
		return anthropicwire.Block{
			Type: "image",
			Source: &anthropicwire.ImageSource{
				Type:      "base64",
				MediaType: mediaType,
				Data:      data,
			},
		}
	}
	return anthropicwire.Block{
		Type:   "image",
		Source: &anthropicwire.ImageSource{Type: "url", URL: url},
	}
}

// bedrockParseDataURI splits a data URI of the form
// "data:<media-type>;base64,<data>" into its media type and payload. ok is
// false for any non-base64 data URI or a plain remote URL.
func bedrockParseDataURI(uri string) (mediaType, data string, ok bool) {
	const prefix = "data:"
	if !strings.HasPrefix(uri, prefix) {
		return "", "", false
	}
	meta, payload, found := strings.Cut(uri[len(prefix):], ",")
	if !found {
		return "", "", false
	}
	mt, encoding, _ := strings.Cut(meta, ";")
	if encoding != "base64" {
		return "", "", false
	}
	return mt, payload, true
}

func (p *Provider) completeAnthropic(ctx context.Context, req core.Request) (*core.Response, error) {
	anthropicReq := buildBedrockAnthropicRequest(req)

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

	var anthropicResp bedrockAnthropicResponse
	if err := json.Unmarshal(output.Body, &anthropicResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	text := ""
	var toolCalls []core.ToolCall
	for _, c := range anthropicResp.Content {
		if c.Type == "text" {
			text += c.Text
			continue
		}
		if c.Type == "tool_use" {
			args := string(c.Input)
			if args == "" {
				args = "{}"
			}
			toolCalls = append(toolCalls, core.ToolCall{
				ID:   c.ID,
				Type: "function",
				Function: core.FunctionCall{
					Name:      c.Name,
					Arguments: args,
				},
			})
		}
	}

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
		},
	}, nil
}
