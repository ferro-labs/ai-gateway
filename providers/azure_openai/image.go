package azureopenai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// imageRequest is the body Azure OpenAI accepts on the images/generations
// endpoint. Azure routes by deployment in the URL, so "model" is not sent.
type imageRequest struct {
	Prompt         string `json:"prompt"`
	N              *int   `json:"n,omitempty"`
	Size           string `json:"size,omitempty"`
	ResponseFormat string `json:"response_format,omitempty"`
	Quality        string `json:"quality,omitempty"`
	Style          string `json:"style,omitempty"`
	User           string `json:"user,omitempty"`
}

// imageResponse mirrors the synchronous OpenAI-shaped image response.
type imageResponse struct {
	Created int64                 `json:"created"`
	Data    []core.GeneratedImage `json:"data"`
	// Usage is sent only by the gpt-image deployments, which Azure bills per
	// token — the catalog carries no per-tile price for azure/gpt-image-*, so
	// not reading this costs every such generation at zero. Azure spells it the
	// way the OpenAI image schema does, not the way core.Usage marshals, hence
	// the local shape.
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

// GenerateImage sends an image generation request to Azure OpenAI. The request
// targets the deployment named by req.Model, falling back to the configured
// deployment. Azure (api-version 2024-10-21) returns the result synchronously —
// no async polling is required.
func (p *Provider) GenerateImage(ctx context.Context, req core.ImageRequest) (*core.ImageResponse, error) {
	if err := core.EnforceImageResponseFormat(Name, req); err != nil {
		return nil, err
	}
	pReq := imageRequest{
		Prompt:         req.Prompt,
		N:              req.N,
		Size:           req.Size,
		ResponseFormat: req.ResponseFormat,
		Quality:        req.Quality,
		Style:          req.Style,
		User:           req.User,
	}
	bodyReader, _, release, err := core.JSONBodyReader(pReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal image request: %w", err)
	}
	defer release()

	url, err := p.opEndpoint(p.deploymentFor(req.Model), "images/generations")
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("api-key", p.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	respBody, err := core.ReadResponseBody(httpResp.Body, core.MaxProviderResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	if httpResp.StatusCode != http.StatusOK {
		return nil, core.APIErrorFromResponse("azure openai", httpResp, respBody)
	}

	var pResp imageResponse
	if err := json.Unmarshal(respBody, &pResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal image response: %w", err)
	}
	return &core.ImageResponse{
		Created: pResp.Created,
		Data:    pResp.Data,
		Usage: core.Usage{
			PromptTokens:     pResp.Usage.InputTokens,
			CompletionTokens: pResp.Usage.OutputTokens,
			TotalTokens:      pResp.Usage.TotalTokens,
		},
	}, nil
}
