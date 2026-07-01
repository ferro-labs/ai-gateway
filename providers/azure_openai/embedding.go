package azureopenai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// embeddingRequest is the OpenAI-shaped body Azure OpenAI accepts on the
// /embeddings endpoint. The deployment in the URL is authoritative, so "model"
// is ignored by Azure but harmless to send.
type embeddingRequest struct {
	Model          string `json:"model,omitempty"`
	Input          any    `json:"input"`
	EncodingFormat string `json:"encoding_format,omitempty"`
	Dimensions     *int   `json:"dimensions,omitempty"`
	User           string `json:"user,omitempty"`
}

// embeddingResponse mirrors the OpenAI-shaped embedding response.
type embeddingResponse struct {
	Object string              `json:"object"`
	Data   []core.Embedding    `json:"data"`
	Model  string              `json:"model"`
	Usage  core.EmbeddingUsage `json:"usage"`
}

// Embed sends an embedding request to Azure OpenAI. The request targets the
// deployment named by req.Model, falling back to the configured deployment.
func (p *Provider) Embed(ctx context.Context, req core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	input, err := normalizeEmbeddingInput(req.Input)
	if err != nil {
		return nil, err
	}
	switch req.EncodingFormat {
	case "", "float", "base64":
		// accepted
	default:
		return nil, fmt.Errorf("embed: unsupported encoding_format %q; valid values are \"float\" and \"base64\"", req.EncodingFormat)
	}

	pReq := embeddingRequest{
		Model:          req.Model,
		Input:          input,
		EncodingFormat: req.EncodingFormat,
		Dimensions:     req.Dimensions,
		User:           req.User,
	}
	bodyReader, _, release, err := core.JSONBodyReader(pReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal embedding request: %w", err)
	}
	defer release()

	url := p.opEndpoint(p.deploymentFor(req.Model), "embeddings")
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
		var errResp azureOpenAIErrorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return nil, fmt.Errorf("azure openai API error (%d): %s", httpResp.StatusCode, errResp.Error.Message)
		}
		return nil, fmt.Errorf("azure openai API error (%d): %s", httpResp.StatusCode, string(respBody))
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

// normalizeEmbeddingInput validates and normalizes the polymorphic Input field
// into a string or []string, rejecting empty/nil/non-string inputs.
func normalizeEmbeddingInput(input any) (any, error) {
	switch v := input.(type) {
	case string:
		if v == "" {
			return nil, fmt.Errorf("embed: input string must not be empty")
		}
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
