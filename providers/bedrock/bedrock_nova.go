package bedrock

import (
	"context"
	"strings"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

type bedrockNovaRequest struct {
	SchemaVersion   string                      `json:"schemaVersion"`
	Messages        []bedrockNovaMessage        `json:"messages"`
	System          []bedrockNovaTextBlock      `json:"system,omitempty"`
	InferenceConfig *bedrockNovaInferenceConfig `json:"inferenceConfig,omitempty"`
}

type bedrockNovaMessage struct {
	Role    string                 `json:"role"`
	Content []bedrockNovaTextBlock `json:"content"`
}

type bedrockNovaTextBlock struct {
	Text string `json:"text"`
}

type bedrockNovaInferenceConfig struct {
	MaxTokens     int      `json:"maxTokens,omitempty"`
	Temperature   *float64 `json:"temperature,omitempty"`
	TopP          *float64 `json:"topP,omitempty"`
	StopSequences []string `json:"stopSequences,omitempty"`
}

type bedrockNovaResponse struct {
	Output struct {
		Message struct {
			Role    string `json:"role"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	} `json:"output"`
	StopReason string `json:"stopReason"`
	Usage      struct {
		InputTokens  int `json:"inputTokens"`
		OutputTokens int `json:"outputTokens"`
		TotalTokens  int `json:"totalTokens"`
	} `json:"usage"`
}

func (p *Provider) completeNova(ctx context.Context, req core.Request) (*core.Response, error) {
	novaReq := bedrockNovaRequest{
		SchemaVersion: "messages-v1",
	}
	for _, msg := range req.Messages {
		content := bedrockNovaMessageTextContent(msg)
		if msg.Role == core.RoleSystem {
			novaReq.System = append(novaReq.System, content...)
			continue
		}
		novaReq.Messages = append(novaReq.Messages, bedrockNovaMessage{
			Role:    msg.Role,
			Content: content,
		})
	}

	if req.MaxTokens != nil || req.Temperature != nil || req.TopP != nil || len(req.Stop) > 0 {
		novaReq.InferenceConfig = &bedrockNovaInferenceConfig{
			Temperature:   req.Temperature,
			TopP:          req.TopP,
			StopSequences: req.Stop,
		}
		if req.MaxTokens != nil {
			novaReq.InferenceConfig.MaxTokens = *req.MaxTokens
		}
	}

	var novaResp bedrockNovaResponse
	if err := p.invokeModelJSON(ctx, req.Model, novaReq, &novaResp); err != nil {
		return nil, err
	}

	var text strings.Builder
	for _, c := range novaResp.Output.Message.Content {
		text.WriteString(c.Text)
	}

	totalTokens := novaResp.Usage.TotalTokens
	if totalTokens == 0 {
		totalTokens = novaResp.Usage.InputTokens + novaResp.Usage.OutputTokens
	}

	return &core.Response{
		Model:    req.Model,
		Provider: p.name,
		Choices: []core.Choice{{
			Index:        0,
			Message:      core.Message{Role: core.RoleAssistant, Content: text.String()},
			FinishReason: core.NormalizeFinishReason(novaResp.StopReason),
		}},
		Usage: core.Usage{
			PromptTokens:     novaResp.Usage.InputTokens,
			CompletionTokens: novaResp.Usage.OutputTokens,
			TotalTokens:      totalTokens,
		},
	}, nil
}

func bedrockNovaMessageTextContent(msg core.Message) []bedrockNovaTextBlock {
	if len(msg.ContentParts) == 0 {
		return []bedrockNovaTextBlock{{Text: msg.Content}}
	}

	content := make([]bedrockNovaTextBlock, 0, len(msg.ContentParts))
	for _, part := range msg.ContentParts {
		if part.Type == core.ContentTypeText {
			content = append(content, bedrockNovaTextBlock{Text: part.Text})
		}
	}
	if len(content) == 0 && msg.Content != "" {
		content = append(content, bedrockNovaTextBlock{Text: msg.Content})
	}
	return content
}
