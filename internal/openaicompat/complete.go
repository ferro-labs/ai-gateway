package openaicompat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// ChatResponse is the OpenAI-shaped non-streaming chat completion response body.
type ChatResponse struct {
	ID      string        `json:"id"`
	Model   string        `json:"model"`
	Choices []core.Choice `json:"choices"`
	Usage   core.Usage    `json:"usage"`
}

// APIError builds a provider error from a non-200 response body. It delegates to
// core.APIError, which uses the OpenAI {"error":{"message":…}} envelope when
// present and falls back to the raw body. label is the human-facing provider name.
func APIError(label string, status int, body []byte) error {
	return core.APIError(label, status, body)
}

// ChatParams configures a request to an OpenAI-compatible chat endpoint.
type ChatParams struct {
	HTTPClient *http.Client
	URL        string            // full chat-completions endpoint URL
	Headers    map[string]string // auth + content-type
	Provider   string            // sets core.Response.Provider
	Label      string            // human-facing name for error messages
}

func newChatRequest(ctx context.Context, p ChatParams, req core.Request, stream bool) (*http.Response, func(), error) {
	bodyReader, _, release, err := BuildBody(req, stream)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.URL, bodyReader) //nolint:gosec // URL derived from a base URL validated at construction
	if err != nil {
		release()
		return nil, nil, fmt.Errorf("failed to create request: %w", err)
	}
	for k, v := range p.Headers {
		httpReq.Header.Set(k, v)
	}
	httpResp, err := p.HTTPClient.Do(httpReq) //nolint:gosec // see above
	if err != nil {
		release()
		return nil, nil, fmt.Errorf("request failed: %w", err)
	}
	return httpResp, release, nil
}

// PostChat sends a non-streaming OpenAI-compatible chat completion and decodes
// the canonical response. Providers with extended response fields (e.g. DeepSeek
// cache/reasoning usage) should decode the body themselves instead.
func PostChat(ctx context.Context, p ChatParams, req core.Request) (*core.Response, error) {
	httpResp, release, err := newChatRequest(ctx, p, req, false)
	if err != nil {
		return nil, err
	}
	defer release()
	defer func() { _ = httpResp.Body.Close() }()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	if httpResp.StatusCode != http.StatusOK {
		return nil, APIError(p.Label, httpResp.StatusCode, respBody)
	}

	var pResp ChatResponse
	if err := json.Unmarshal(respBody, &pResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}
	return &core.Response{
		ID:       pResp.ID,
		Model:    pResp.Model,
		Provider: p.Provider,
		Choices:  pResp.Choices,
		Usage:    pResp.Usage,
	}, nil
}

// PostStream sends a streaming OpenAI-compatible chat completion and returns a
// channel of decoded chunks (see StreamSSE). The non-200 body is drained and
// surfaced as an error before any goroutine is started.
func PostStream(ctx context.Context, p ChatParams, req core.Request) (<-chan core.StreamChunk, error) {
	httpResp, release, err := newChatRequest(ctx, p, req, true)
	if err != nil {
		return nil, err
	}
	release()

	if httpResp.StatusCode != http.StatusOK {
		defer func() { _ = httpResp.Body.Close() }()
		respBody, _ := io.ReadAll(httpResp.Body)
		return nil, APIError(p.Label, httpResp.StatusCode, respBody)
	}
	return StreamSSE(httpResp.Body), nil
}
