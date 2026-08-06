package mistral

import (
	"context"

	"github.com/ferro-labs/ai-gateway/providers/core"
	"github.com/ferro-labs/ai-gateway/providers/core/openaicompat"
)

// Moderate classifies content via Mistral's OpenAI-compatible /v1/moderations
// endpoint. Mistral's response carries per-category booleans and scores but omits
// the OpenAI `flagged` field, so it is derived here — a result is flagged when
// any category is true — to keep the surface faithful to the OpenAI contract
// (`flagged` = "whether any of the categories are flagged"). Without this a
// client reading only `flagged` would see false on content Mistral did flag.
func (p *Provider) Moderate(ctx context.Context, req core.ModerationRequest) (*core.ModerationResponse, error) {
	resp, err := openaicompat.PostModeration(ctx, openaicompat.ModerationParams{
		HTTPClient: p.httpClient,
		URL:        p.baseURL + "/moderations",
		Headers:    map[string]string{"Authorization": "Bearer " + p.apiKey, "Content-Type": "application/json"},
		Label:      Name,
	}, req)
	if err != nil {
		return nil, err
	}
	for i := range resp.Results {
		for _, flagged := range resp.Results[i].Categories {
			if flagged {
				resp.Results[i].Flagged = true
				break
			}
		}
	}
	return resp, nil
}
