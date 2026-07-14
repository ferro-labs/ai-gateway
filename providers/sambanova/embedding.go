package sambanova

import (
	"context"

	"github.com/ferro-labs/ai-gateway/providers/core"
	"github.com/ferro-labs/ai-gateway/providers/internal/openaicompat"
)

// Embed sends an OpenAI-compatible embedding request to SambaNova.
func (p *Provider) Embed(ctx context.Context, req core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return openaicompat.PostEmbeddings(ctx, openaicompat.EmbeddingParams{
		HTTPClient: p.httpClient,
		URL:        p.baseURL + "/embeddings",
		Headers:    map[string]string{"Authorization": "Bearer " + p.apiKey, "Content-Type": "application/json"},
		Label:      "sambanova",
	}, req)
}
