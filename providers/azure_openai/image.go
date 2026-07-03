package azureopenai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
}

// GenerateImage sends an image generation request to Azure OpenAI. The request
// targets the deployment named by req.Model, falling back to the configured
// deployment. Azure (api-version 2024-10-21) returns the result synchronously —
// no async polling is required.
func (p *Provider) GenerateImage(ctx context.Context, req core.ImageRequest) (*core.ImageResponse, error) {
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

	url := p.opEndpoint(p.deploymentFor(req.Model), "images/generations")
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

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	if httpResp.StatusCode != http.StatusOK {
		return nil, core.APIError("azure openai", httpResp.StatusCode, respBody)
	}

	var pResp imageResponse
	if err := json.Unmarshal(respBody, &pResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal image response: %w", err)
	}
	return &core.ImageResponse{
		Created: pResp.Created,
		Data:    pResp.Data,
	}, nil
}
