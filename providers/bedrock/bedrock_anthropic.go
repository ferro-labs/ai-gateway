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
	AnthropicVersion string                    `json:"anthropic_version"`
	MaxTokens        int                       `json:"max_tokens"`
	Messages         []bedrockAnthropicMessage `json:"messages"`
	Tools            []anthropicwire.Tool      `json:"tools,omitempty"`
	ToolChoice       any                       `json:"tool_choice,omitempty"`
	Temperature      *float64                  `json:"temperature,omitempty"`
	TopP             *float64                  `json:"top_p,omitempty"`
	StopSequences    []string                  `json:"stop_sequences,omitempty"`
	System           string                    `json:"system,omitempty"`
}

type bedrockAnthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type bedrockAnthropicBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
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

	messages, system := bedrockBuildAnthropicMessages(req)

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

func bedrockBuildAnthropicMessages(req core.Request) ([]bedrockAnthropicMessage, string) {
	var systemParts []string
	var messages []bedrockAnthropicMessage
	for _, msg := range req.Messages {
		switch msg.Role {
		case core.RoleSystem:
			systemParts = append(systemParts, msg.Content)
		case core.RoleTool:
			block := bedrockAnthropicBlock{
				Type:      "tool_result",
				ToolUseID: msg.ToolCallID,
				Content:   msg.Content,
			}
			if n := len(messages); n > 0 && messages[n-1].Role == core.RoleUser {
				if blocks, ok := messages[n-1].Content.([]bedrockAnthropicBlock); ok {
					blocks = append(blocks, block)
					messages[n-1].Content = blocks
					continue
				}
			}
			messages = append(messages, bedrockAnthropicMessage{Role: core.RoleUser, Content: []bedrockAnthropicBlock{block}})
		default:
			messages = append(messages, bedrockAnthropicMessage{Role: msg.Role, Content: bedrockAnthropicContent(msg)})
		}
	}
	return messages, strings.Join(systemParts, "\n")
}

func bedrockAnthropicContent(msg core.Message) any {
	var blocks []bedrockAnthropicBlock
	if msg.Content != "" {
		blocks = append(blocks, bedrockAnthropicBlock{Type: "text", Text: msg.Content})
	}
	for _, tc := range msg.ToolCalls {
		input := json.RawMessage(tc.Function.Arguments)
		if len(input) == 0 || !json.Valid(input) {
			input = json.RawMessage(`{}`)
		}
		blocks = append(blocks, bedrockAnthropicBlock{
			Type:  "tool_use",
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Input: input,
		})
	}
	if len(msg.ToolCalls) == 0 {
		return msg.Content
	}
	return blocks
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
