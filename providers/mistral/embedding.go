package mistral

import (
	"context"
	"fmt"

	"github.com/ferro-labs/ai-gateway/internal/openaicompat"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// mistralEmbeddingBody reshapes the OpenAI-shaped embeddings body for Mistral,
// which expects "output_dimension" instead of the standard "dimensions" field.
type mistralEmbeddingBody struct {
	Model           string `json:"model"`
	Input           any    `json:"input"`
	EncodingFormat  string `json:"encoding_format,omitempty"`
	OutputDimension *int   `json:"output_dimension,omitempty"`
	User            string `json:"user,omitempty"`
}

// mistralEmbeddingTransform maps core.EmbeddingRequest onto Mistral's embeddings
// body, renaming dimensions → output_dimension. input is the normalized wire
// form (string or []string) produced by the shared helper.
func mistralEmbeddingTransform(req core.EmbeddingRequest, input any) any {
	return mistralEmbeddingBody{
		Model:           req.Model,
		Input:           input,
		EncodingFormat:  req.EncodingFormat,
		OutputDimension: req.Dimensions,
		User:            req.User,
	}
}

// Embed sends an OpenAI-compatible embedding request to Mistral.
func (p *Provider) Embed(ctx context.Context, req core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	if req.EncodingFormat != "" && req.EncodingFormat != "float" {
		return nil, fmt.Errorf("embed: unsupported encoding_format %q; valid value is \"float\"", req.EncodingFormat)
	}
	return openaicompat.PostEmbeddings(ctx, openaicompat.EmbeddingParams{
		HTTPClient:    p.httpClient,
		URL:           p.baseURL + "/v1/embeddings",
		Headers:       map[string]string{"Authorization": "Bearer " + p.apiKey, "Content-Type": "application/json"},
		Label:         "mistral",
		BodyTransform: mistralEmbeddingTransform,
	}, req)
}
