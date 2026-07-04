package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

type geminiEmbeddingContent struct {
	Parts []geminiPart `json:"parts"`
}

type geminiBatchEmbedContentRequest struct {
	Model                string                 `json:"model"`
	Content              geminiEmbeddingContent `json:"content"`
	OutputDimensionality *int                   `json:"outputDimensionality,omitempty"`
}

type geminiBatchEmbedRequest struct {
	Requests []geminiBatchEmbedContentRequest `json:"requests"`
}

type geminiBatchEmbedResponse struct {
	Embeddings []struct {
		Values []float64 `json:"values"`
	} `json:"embeddings"`
	UsageMetadata struct {
		PromptTokenCount int `json:"promptTokenCount"`
		TotalTokenCount  int `json:"totalTokenCount"`
	} `json:"usageMetadata"`
}

func geminiModelResource(model string) string {
	model = strings.TrimPrefix(model, "models/")
	return "models/" + model
}

// Embed sends a text embedding request to Gemini's batchEmbedContents endpoint.
func (p *Provider) Embed(ctx context.Context, req core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	if err := core.ValidateEmbeddingEncodingFormat(req.EncodingFormat); err != nil {
		return nil, err
	}
	texts, err := core.CoerceEmbeddingInput(req.Input)
	if err != nil {
		return nil, err
	}

	model := strings.TrimPrefix(req.Model, "models/")
	modelResource := geminiModelResource(req.Model)
	geminiReq := geminiBatchEmbedRequest{
		Requests: make([]geminiBatchEmbedContentRequest, 0, len(texts)),
	}
	for _, text := range texts {
		geminiReq.Requests = append(geminiReq.Requests, geminiBatchEmbedContentRequest{
			Model:                modelResource,
			Content:              geminiEmbeddingContent{Parts: []geminiPart{{Text: text}}},
			OutputDimensionality: req.Dimensions,
		})
	}

	url := fmt.Sprintf("%s/v1beta/models/%s:batchEmbedContents", p.baseURL, url.PathEscape(model))
	httpResp, release, err := p.doJSONRequest(ctx, http.MethodPost, url, "embed ", geminiReq)
	if err != nil {
		return nil, err
	}
	defer release()
	defer func() { _ = httpResp.Body.Close() }()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read embed response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		return nil, core.APIError("gemini embed", httpResp.StatusCode, respBody)
	}

	var geminiResp geminiBatchEmbedResponse
	if err := json.Unmarshal(respBody, &geminiResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal embed response: %w", err)
	}
	if len(geminiResp.Embeddings) != len(texts) {
		return nil, fmt.Errorf("gemini embed API returned %d embeddings for %d inputs", len(geminiResp.Embeddings), len(texts))
	}

	data := make([]core.Embedding, len(geminiResp.Embeddings))
	for i, embedding := range geminiResp.Embeddings {
		data[i] = core.Embedding{
			Object:    "embedding",
			Embedding: embedding.Values,
			Index:     i,
		}
	}

	totalTokens := geminiResp.UsageMetadata.TotalTokenCount
	if totalTokens == 0 {
		totalTokens = geminiResp.UsageMetadata.PromptTokenCount
	}
	return &core.EmbeddingResponse{
		Object: "list",
		Data:   data,
		Model:  req.Model,
		Usage: core.EmbeddingUsage{
			PromptTokens: geminiResp.UsageMetadata.PromptTokenCount,
			TotalTokens:  totalTokens,
		},
	}, nil
}
