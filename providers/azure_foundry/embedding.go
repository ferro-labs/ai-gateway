package azurefoundry

import (
	"context"

	"github.com/ferro-labs/ai-gateway/providers/core"
	"github.com/ferro-labs/ai-gateway/providers/core/openaicompat"
)

// Embed sends an OpenAI-compatible embedding request to Azure AI Foundry. It
// targets the GA /openai/v1/embeddings route — the same base and api-key auth
// the chat surface already uses — so text-embedding-3-* deployments are reachable
// through the typed /v1/embeddings surface rather than only the sibling
// azure-openai provider. Azure Foundry declares NonOpenAIWire(), so the
// transparent /v1/* pass-through cannot serve embeddings for it; this method is
// the only path.
func (p *Provider) Embed(ctx context.Context, req core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	if err := core.ValidateEmbeddingEncodingFormat(req.EncodingFormat); err != nil {
		return nil, err
	}
	return openaicompat.PostEmbeddings(ctx, openaicompat.EmbeddingParams{
		HTTPClient: p.httpClient,
		URL:        p.baseURL + "/openai/v1/embeddings",
		Headers:    map[string]string{"api-key": p.apiKey, "Content-Type": "application/json"},
		Label:      "azure foundry",
	}, req)
}
