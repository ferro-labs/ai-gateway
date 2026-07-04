package together

import (
	"context"

	"github.com/ferro-labs/ai-gateway/providers/core"
	"github.com/ferro-labs/ai-gateway/providers/internal/openaicompat"
)

// Embed sends an OpenAI-compatible embedding request to Together AI.
func (p *Provider) Embed(ctx context.Context, req core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	if err := core.ValidateEmbeddingEncodingFormat(req.EncodingFormat); err != nil {
		return nil, err
	}
	return openaicompat.PostEmbeddings(ctx, openaicompat.EmbeddingParams{
		HTTPClient: p.httpClient,
		URL:        p.baseURL + "/v1/embeddings",
		Headers:    p.headers(),
		Label:      "together",
	}, req)
}
