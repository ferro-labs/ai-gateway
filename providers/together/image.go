package together

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// togetherImageRequest is the OpenAI-compatible image-generation body Together AI
// accepts on /images/generations (FLUX models).
type togetherImageRequest struct {
	Model          string `json:"model"`
	Prompt         string `json:"prompt"`
	N              *int   `json:"n,omitempty"`
	Size           string `json:"size,omitempty"`
	ResponseFormat string `json:"response_format,omitempty"`
	User           string `json:"user,omitempty"`
}

// GenerateImage sends an image generation request to Together AI's
// OpenAI-compatible /images/generations endpoint (FLUX).
func (p *Provider) GenerateImage(ctx context.Context, req core.ImageRequest) (*core.ImageResponse, error) {
	if err := core.EnforceImageResponseFormat(p.name, req); err != nil {
		return nil, err
	}

	payload := togetherImageRequest{
		Model:          req.Model,
		Prompt:         req.Prompt,
		N:              req.N,
		Size:           req.Size,
		ResponseFormat: req.ResponseFormat,
		User:           req.User,
	}

	body, contentLen, release, err := core.JSONBodyReader(payload)
	if err != nil {
		return nil, fmt.Errorf("together: failed to marshal image request: %w", err)
	}
	defer release()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/images/generations", body)
	if err != nil {
		return nil, fmt.Errorf("together: failed to create image request: %w", err)
	}
	httpReq.ContentLength = int64(contentLen)
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("together: image request failed: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	respBody, err := core.ReadResponseBody(httpResp.Body, core.MaxProviderResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("together: failed to read image response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		return nil, core.APIErrorFromResponse("together", httpResp, respBody)
	}

	var decoded core.ImageResponse
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return nil, fmt.Errorf("together: failed to decode image response: %w", err)
	}

	if decoded.Created == 0 {
		decoded.Created = time.Now().Unix()
	}

	return &decoded, nil
}
