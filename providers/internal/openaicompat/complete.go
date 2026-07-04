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

	// BodyTransform, when set, reshapes the outgoing request body (e.g. to rename
	// a wire field like Mistral's seed→random_seed) while keeping the shared
	// response decoding, error handling, and finish_reason normalization. It
	// receives the request with Stream and StreamOptions already applied.
	BodyTransform func(core.Request) any

	// ExtraResponseFields, when set, captures these top-level response fields
	// (e.g. Perplexity's "citations", "search_results") into core.Response.Metadata,
	// surfacing provider-specific data the canonical response shape doesn't model.
	ExtraResponseFields []string
}

func newChatRequest(ctx context.Context, p ChatParams, req core.Request, stream bool) (*http.Response, func(), error) {
	var (
		bodyReader io.Reader
		release    func()
		err        error
	)
	if p.BodyTransform != nil {
		req.Stream = stream
		bodyReader, _, release, err = core.JSONBodyReader(p.BodyTransform(req))
	} else {
		bodyReader, _, release, err = BuildBody(req, stream)
	}
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
	// Normalize provider-specific finish reasons (e.g. Mistral's model_length)
	// to the canonical OpenAI vocabulary for every OpenAI-compatible provider.
	for i := range pResp.Choices {
		pResp.Choices[i].FinishReason = core.NormalizeFinishReason(pResp.Choices[i].FinishReason)
	}
	resp := &core.Response{
		ID:       pResp.ID,
		Model:    pResp.Model,
		Provider: p.Provider,
		Choices:  pResp.Choices,
		Usage:    pResp.Usage,
	}
	if meta := captureExtraFields(respBody, p.ExtraResponseFields); meta != nil {
		resp.Metadata = meta
	}
	return resp, nil
}

// captureExtraFields decodes the named top-level response fields into a metadata
// map, used to surface provider-specific data (e.g. Perplexity citations) that
// the canonical response shape does not model. Returns nil when nothing matches.
func captureExtraFields(body []byte, fields []string) map[string]any {
	if len(fields) == 0 {
		return nil
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(body, &raw) != nil {
		return nil
	}
	meta := make(map[string]any, len(fields))
	for _, k := range fields {
		rawVal, ok := raw[k]
		if !ok {
			continue
		}
		var v any
		if json.Unmarshal(rawVal, &v) == nil {
			meta[k] = v
		}
	}
	if len(meta) == 0 {
		return nil
	}
	return meta
}

// PostStream sends a streaming OpenAI-compatible chat completion and returns a
// channel of decoded chunks (see StreamSSE). The non-200 body is drained and
// surfaced as an error before any goroutine is started.
func PostStream(ctx context.Context, p ChatParams, req core.Request) (<-chan core.StreamChunk, error) {
	// Request a terminal usage chunk for cost/metrics tracking unless the caller
	// already configured stream_options.
	if req.StreamOptions == nil {
		req.StreamOptions = &core.StreamOptions{IncludeUsage: true}
	}
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
