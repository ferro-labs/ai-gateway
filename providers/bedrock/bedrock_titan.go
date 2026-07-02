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

type bedrockTitanRequest struct {
	InputText            string `json:"inputText"`
	TextGenerationConfig struct {
		MaxTokenCount int      `json:"maxTokenCount,omitempty"`
		Temperature   *float64 `json:"temperature,omitempty"`
		TopP          *float64 `json:"topP,omitempty"`
		StopSequences []string `json:"stopSequences,omitempty"`
	} `json:"textGenerationConfig"`
}

type bedrockTitanResponse struct {
	InputTextTokenCount int `json:"inputTextTokenCount"`
	Results             []struct {
		TokenCount       int    `json:"tokenCount"`
		OutputText       string `json:"outputText"`
		CompletionReason string `json:"completionReason"`
	} `json:"results"`
}

func (p *Provider) completeTitan(ctx context.Context, req core.Request) (*core.Response, error) {
	var sb strings.Builder
	for _, msg := range req.Messages {
		sb.WriteString(msg.Content)
		sb.WriteString("\n")
	}

	titanReq := bedrockTitanRequest{InputText: sb.String()}
	if req.MaxTokens != nil {
		titanReq.TextGenerationConfig.MaxTokenCount = *req.MaxTokens
	}
	titanReq.TextGenerationConfig.Temperature = req.Temperature
	titanReq.TextGenerationConfig.TopP = req.TopP
	titanReq.TextGenerationConfig.StopSequences = req.Stop

	body, err := core.MarshalJSON(titanReq)
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

	var titanResp bedrockTitanResponse
	if err := json.Unmarshal(output.Body, &titanResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	var choices []core.Choice
	for i, result := range titanResp.Results {
		choices = append(choices, core.Choice{
			Index:        i,
			Message:      core.Message{Role: core.RoleAssistant, Content: result.OutputText},
			FinishReason: core.NormalizeFinishReason(result.CompletionReason),
		})
	}

	totalCompletion := 0
	for _, r := range titanResp.Results {
		totalCompletion += r.TokenCount
	}

	return &core.Response{
		Model:    req.Model,
		Provider: p.name,
		Choices:  choices,
		Usage: core.Usage{
			PromptTokens:     titanResp.InputTextTokenCount,
			CompletionTokens: totalCompletion,
		},
	}, nil
}
