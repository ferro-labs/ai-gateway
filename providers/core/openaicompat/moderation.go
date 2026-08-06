package openaicompat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// ModerationParams configures a request to an OpenAI-compatible moderations endpoint.
type ModerationParams struct {
	HTTPClient *http.Client
	URL        string            // full moderations endpoint URL
	Headers    map[string]string // auth + content-type
	Label      string            // human-facing name for error messages
}

// moderationBody is the OpenAI-shaped moderations request body.
type moderationBody struct {
	Input any    `json:"input"`
	Model string `json:"model,omitempty"`
}

// PostModeration sends an OpenAI-compatible moderations request and decodes the
// canonical response. Both providers that offer moderation (OpenAI, Mistral)
// speak this shape verbatim, so it is a passthrough with no per-provider
// translation.
func PostModeration(ctx context.Context, p ModerationParams, req core.ModerationRequest) (*core.ModerationResponse, error) {
	bodyReader, _, release, err := core.JSONBodyReader(moderationBody{Input: req.Input, Model: req.Model})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal moderation request: %w", err)
	}
	defer release()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.URL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	for k, v := range p.Headers {
		httpReq.Header.Set(k, v)
	}

	httpResp, err := p.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	respBody, err := core.ReadResponseBody(httpResp.Body, core.MaxProviderResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
		return nil, APIErrorFromResponse(p.Label, httpResp, respBody)
	}

	var resp core.ModerationResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal moderation response: %w", err)
	}
	return &resp, nil
}
