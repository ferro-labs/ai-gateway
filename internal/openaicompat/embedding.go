package openaicompat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// EmbeddingParams configures a request to an OpenAI-compatible embeddings endpoint.
type EmbeddingParams struct {
	HTTPClient *http.Client
	URL        string            // full embeddings endpoint URL
	Headers    map[string]string // auth + content-type
	Label      string            // human-facing name for error messages
}

// embeddingBody is the OpenAI-shaped embeddings request body.
type embeddingBody struct {
	Model          string `json:"model"`
	Input          any    `json:"input"`
	EncodingFormat string `json:"encoding_format,omitempty"`
	Dimensions     *int   `json:"dimensions,omitempty"`
	User           string `json:"user,omitempty"`
}

// embeddingResponse mirrors the OpenAI /v1/embeddings response body.
type embeddingResponse struct {
	Object string              `json:"object"`
	Data   []core.Embedding    `json:"data"`
	Model  string              `json:"model"`
	Usage  core.EmbeddingUsage `json:"usage"`
}

// PostEmbeddings sends an OpenAI-compatible embeddings request and decodes the
// canonical response. req.Input is normalised to preserve its wire form: a bare
// string stays a string and a []string stays an array; empty arrays, nil, and
// non-string values are rejected. Providers that forward extra request fields or
// decode extended usage should build the request themselves instead.
func PostEmbeddings(ctx context.Context, p EmbeddingParams, req core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	input, err := normalizeEmbeddingInput(req.Input)
	if err != nil {
		return nil, err
	}

	bodyReader, _, release, err := core.JSONBodyReader(embeddingBody{
		Model:          req.Model,
		Input:          input,
		EncodingFormat: req.EncodingFormat,
		Dimensions:     req.Dimensions,
		User:           req.User,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal embedding request: %w", err)
	}
	defer release()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.URL, bodyReader) //nolint:gosec // URL derived from a base URL validated at construction
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	for k, v := range p.Headers {
		httpReq.Header.Set(k, v)
	}

	httpResp, err := p.HTTPClient.Do(httpReq) //nolint:gosec // see above
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
		return nil, APIError(p.Label, httpResp.StatusCode, respBody)
	}

	var pResp embeddingResponse
	if err := json.Unmarshal(respBody, &pResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal embedding response: %w", err)
	}
	return &core.EmbeddingResponse{
		Object: pResp.Object,
		Data:   pResp.Data,
		Model:  pResp.Model,
		Usage:  pResp.Usage,
	}, nil
}

// normalizeEmbeddingInput validates the polymorphic embeddings Input, preserving
// its wire form: a bare string stays a string and a []string stays an array.
// Empty arrays, nil, and non-string values are rejected.
func normalizeEmbeddingInput(input any) (any, error) {
	switch v := input.(type) {
	case string:
		return v, nil
	case []string:
		if len(v) == 0 {
			return nil, fmt.Errorf("embed: Input must not be an empty array")
		}
		return v, nil
	case []any:
		if len(v) == 0 {
			return nil, fmt.Errorf("embed: Input must not be an empty array")
		}
		strs := make([]string, 0, len(v))
		for i, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("embed: Input[%d] is %T, want string", i, item)
			}
			strs = append(strs, s)
		}
		return strs, nil
	case nil:
		return nil, fmt.Errorf("embed: Input must not be nil")
	default:
		return nil, fmt.Errorf("embed: unsupported Input type %T; want string or []string", input)
	}
}
