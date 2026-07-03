package xai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/logging"
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
	User           string `json:"user,omitempty"`
}

// GenerateImage sends an image generation request to xAI (Grok image models).
// It is OpenAI-compatible against the /images/generations endpoint.
func (p *Provider) GenerateImage(ctx context.Context, req core.ImageRequest) (*core.ImageResponse, error) {
	// grok-2-image ignores size/quality/style. Surface the drop instead of
	// discarding it silently so the caller can observe the mismatch.
	if dropped := droppedImageParams(req); len(dropped) > 0 {
		logging.FromContext(ctx).Warn(
			"xai image models ignore size/quality/style request parameter(s); dropping",
			"provider", p.name,
			"model", req.Model,
			"dropped_params", dropped,
		)
	}

	payload := xaiImageRequest{
		Model:          req.Model,
		Prompt:         req.Prompt,
		N:              req.N,
		ResponseFormat: req.ResponseFormat,
		User:           req.User,
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

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("xai: failed to read image response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		return nil, core.APIError("xai", httpResp.StatusCode, respBody)
	}

	var decoded core.ImageResponse
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return nil, fmt.Errorf("xai: failed to decode image response: %w", err)
	}

	if decoded.Created == 0 {
		decoded.Created = time.Now().Unix()
	}

	return &decoded, nil
}

// droppedImageParams returns, in stable order, the image request parameters that
// xAI's Grok image models ignore but that the caller populated.
func droppedImageParams(req core.ImageRequest) []string {
	var dropped []string
	if req.Size != "" {
		dropped = append(dropped, "size")
	}
	if req.Quality != "" {
		dropped = append(dropped, "quality")
	}
	if req.Style != "" {
		dropped = append(dropped, "style")
	}
	return dropped
}
