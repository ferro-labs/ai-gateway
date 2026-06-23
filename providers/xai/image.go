package xai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// xaiImageRequest is the OpenAI-compatible request body for xAI image
// generation. grok-2-image rejects size/quality/style, so those fields are
// intentionally omitted.
type xaiImageRequest struct {
	Model          string `json:"model"`
	Prompt         string `json:"prompt"`
	N              *int   `json:"n,omitempty"`
	ResponseFormat string `json:"response_format,omitempty"`
}

// xaiImageResponse is the OpenAI-compatible response body for xAI image
// generation.
type xaiImageResponse struct {
	Created int64 `json:"created"`
	Data    []struct {
		URL           string `json:"url"`
		B64JSON       string `json:"b64_json"`
		RevisedPrompt string `json:"revised_prompt"`
	} `json:"data"`
}

// xaiImageErrorResponse mirrors the OpenAI-compatible error envelope.
type xaiImageErrorResponse struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

// GenerateImage sends an image generation request to xAI (Grok image models).
// It is OpenAI-compatible against the /images/generations endpoint.
func (p *Provider) GenerateImage(ctx context.Context, req core.ImageRequest) (*core.ImageResponse, error) {
	payload := xaiImageRequest{
		Model:          req.Model,
		Prompt:         req.Prompt,
		N:              req.N,
		ResponseFormat: req.ResponseFormat,
	}

	body, contentLen, release, err := core.JSONBodyReader(payload)
	if err != nil {
		return nil, fmt.Errorf("xai: failed to marshal image request: %w", err)
	}
	defer release()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/images/generations", body)
	if err != nil {
		return nil, fmt.Errorf("xai: failed to create image request: %w", err)
	}
	httpReq.ContentLength = int64(contentLen)
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("xai: image request failed: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	if httpResp.StatusCode != http.StatusOK {
		respBody, readErr := io.ReadAll(httpResp.Body)
		if readErr != nil {
			return nil, fmt.Errorf("xai: failed to read error response: %w", readErr)
		}
		var errResp xaiImageErrorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return nil, fmt.Errorf("xai API error (%d): %s", httpResp.StatusCode, errResp.Error.Message)
		}
		return nil, fmt.Errorf("xai API error (%d): %s", httpResp.StatusCode, string(respBody))
	}

	var decoded xaiImageResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("xai: failed to decode image response: %w", err)
	}

	images := make([]core.GeneratedImage, len(decoded.Data))
	for i, d := range decoded.Data {
		images[i] = core.GeneratedImage{
			URL:           d.URL,
			B64JSON:       d.B64JSON,
			RevisedPrompt: d.RevisedPrompt,
		}
	}

	created := decoded.Created
	if created == 0 {
		created = time.Now().Unix()
	}

	return &core.ImageResponse{
		Created: created,
		Data:    images,
	}, nil
}
