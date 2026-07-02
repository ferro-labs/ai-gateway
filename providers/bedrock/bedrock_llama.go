package bedrock

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

type bedrockLlamaRequest struct {
	Prompt      string   `json:"prompt"`
	MaxGenLen   int      `json:"max_gen_len,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	TopP        *float64 `json:"top_p,omitempty"`
}

type bedrockLlamaResponse struct {
	Generation           string `json:"generation"`
	PromptTokenCount     int    `json:"prompt_token_count"`
	GenerationTokenCount int    `json:"generation_token_count"`
	StopReason           string `json:"stop_reason"`
}

// bedrockLlamaMessageText returns the text content for a message's prompt
// turn. It falls back to extracting text from ContentParts when Content is
// empty (multi-part content arrays), mirroring bedrockNovaMessageTextContent
// in bedrock_nova.go.
func bedrockLlamaMessageText(msg core.Message) string {
	if len(msg.ContentParts) == 0 {
		return msg.Content
	}

	var sb strings.Builder
	for _, part := range msg.ContentParts {
		if part.Type == core.ContentTypeText {
			sb.WriteString(part.Text)
		}
	}
	if sb.Len() == 0 {
		return msg.Content
	}
	return sb.String()
}

func (p *Provider) completeLlama(ctx context.Context, req core.Request) (*core.Response, error) {
	var sb strings.Builder
	sb.WriteString("<|begin_of_text|>")
	for _, msg := range req.Messages {
		fmt.Fprintf(&sb, "<|start_header_id|>%s<|end_header_id|>\n\n%s<|eot_id|>\n", msg.Role, bedrockLlamaMessageText(msg))
	}
	sb.WriteString("<|start_header_id|>assistant<|end_header_id|>\n\n")

	llamaReq := bedrockLlamaRequest{
		Prompt:      sb.String(),
		Temperature: req.Temperature,
		TopP:        req.TopP,
	}
	if req.MaxTokens != nil {
		llamaReq.MaxGenLen = *req.MaxTokens
	}

	body, err := core.MarshalJSON(llamaReq)
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

	var llamaResp bedrockLlamaResponse
	if err := json.Unmarshal(output.Body, &llamaResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &core.Response{
		Model:    req.Model,
		Provider: p.name,
		Choices: []core.Choice{{
			Index:        0,
			Message:      core.Message{Role: core.RoleAssistant, Content: llamaResp.Generation},
			FinishReason: core.NormalizeFinishReason(llamaResp.StopReason),
		}},
		Usage: core.Usage{
			PromptTokens:     llamaResp.PromptTokenCount,
			CompletionTokens: llamaResp.GenerationTokenCount,
			TotalTokens:      llamaResp.PromptTokenCount + llamaResp.GenerationTokenCount,
		},
	}, nil
}
