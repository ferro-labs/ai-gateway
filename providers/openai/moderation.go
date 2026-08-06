package openai

import (
	"context"

	"github.com/ferro-labs/ai-gateway/providers/core"
	"github.com/ferro-labs/ai-gateway/providers/core/openaicompat"
)

// Moderate classifies content via OpenAI's /v1/moderations endpoint.
func (p *Provider) Moderate(ctx context.Context, req core.ModerationRequest) (*core.ModerationResponse, error) {
	return openaicompat.PostModeration(ctx, openaicompat.ModerationParams{
		HTTPClient: p.httpClient,
		URL:        p.baseURL + "/moderations",
		Headers:    map[string]string{"Authorization": "Bearer " + p.apiKey, "Content-Type": "application/json"},
		Label:      Name,
	}, req)
}
