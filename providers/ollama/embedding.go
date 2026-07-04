package ollama

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// ollamaEmbedRequest is the native Ollama /api/embed request schema.
type ollamaEmbedRequest struct {
	Model      string `json:"model"`
	Input      any    `json:"input"` // string or []string
	Dimensions *int   `json:"dimensions,omitempty"`
}

// ollamaEmbedResponse is the native Ollama /api/embed response schema.
type ollamaEmbedResponse struct {
	Model           string      `json:"model"`
	Embeddings      [][]float64 `json:"embeddings"`
	PromptEvalCount int         `json:"prompt_eval_count"`
}

// Embed sends a request to the native Ollama /api/embed endpoint and adapts
// the response to the OpenAI-compatible core.EmbeddingResponse shape.
func (p *Provider) Embed(ctx context.Context, req core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	input, err := normalizeEmbeddingInput(req.Input)
	if err != nil {
		return nil, err
	}
	if err := core.ValidateEmbeddingEncodingFormat(req.EncodingFormat); err != nil {
		return nil, err
	}

	// Ollama's /api/embed accepts a "dimensions" advanced parameter; forward it
	// when the caller requested a specific output dimension (nil is omitted).
	pReq := ollamaEmbedRequest{
		Model:      req.Model,
		Input:      input,
		Dimensions: req.Dimensions,
	}
	bodyReader, _, release, err := core.JSONBodyReader(pReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal embedding request: %w", err)
	}
	defer release()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/api/embed", bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
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
		var errResp ollamaErrorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return nil, fmt.Errorf("ollama API error (%d): %s", httpResp.StatusCode, errResp.Error.Message)
		}
		return nil, fmt.Errorf("ollama API error (%d): %s", httpResp.StatusCode, string(respBody))
	}

	var oResp ollamaEmbedResponse
	if err := json.Unmarshal(respBody, &oResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal embedding response: %w", err)
	}

	data := make([]core.Embedding, 0, len(oResp.Embeddings))
	for i, row := range oResp.Embeddings {
		data = append(data, core.Embedding{
			Object:    "embedding",
			Embedding: row,
			Index:     i,
		})
	}

	return &core.EmbeddingResponse{
		Object: "list",
		Data:   data,
		Model:  oResp.Model,
		Usage: core.EmbeddingUsage{
			PromptTokens: oResp.PromptEvalCount,
			TotalTokens:  oResp.PromptEvalCount,
		},
	}, nil
}

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
