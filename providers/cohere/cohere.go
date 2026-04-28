// Package cohere provides a client for the Cohere API.
package cohere

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	providerhttp "github.com/ferro-labs/ai-gateway/internal/httpclient"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// Name is the canonical provider identifier.
const Name = "cohere"

const defaultBaseURL = "https://api.cohere.com"

// Provider implements the Cohere API client.
type Provider struct {
	name       string
	apiKey     string
	baseURL    string
	httpClient *http.Client
}

// Compile-time interface assertions.
var (
	_ core.Provider          = (*Provider)(nil)
	_ core.StreamProvider    = (*Provider)(nil)
	_ core.ProxiableProvider = (*Provider)(nil)
	_ core.EmbeddingProvider = (*Provider)(nil)
)

// New creates a new Cohere provider.
func New(apiKey, baseURL string) (*Provider, error) {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	return &Provider{
		name:       Name,
		apiKey:     apiKey,
		baseURL:    baseURL,
		httpClient: providerhttp.ForProvider(Name),
	}, nil
}

// Name implements core.Provider.
func (p *Provider) Name() string { return p.name }

// BaseURL implements core.ProxiableProvider.
func (p *Provider) BaseURL() string { return p.baseURL }

// AuthHeaders implements core.ProxiableProvider.
func (p *Provider) AuthHeaders() map[string]string {
	return map[string]string{"Authorization": "Bearer " + p.apiKey}
}

// SupportedModels returns the static list of known Cohere models.
func (p *Provider) SupportedModels() []string {
	return []string{
		"command-r-plus",
		"command-r",
		"command-light",
		"command",
		"embed-v4.0",
		"embed-english-v3.0",
		"embed-multilingual-v3.0",
		"embed-english-light-v3.0",
		"embed-multilingual-light-v3.0",
		"embed-english-v2.0",
		"embed-multilingual-v2.0",
	}
}

// SupportsModel returns true if the model matches a known Cohere prefix.
func (p *Provider) SupportsModel(model string) bool {
	for _, prefix := range []string{"command", "embed-", "rerank-"} {
		if strings.HasPrefix(model, prefix) {
			return true
		}
	}
	return false
}

// Models returns structured model metadata for the /v1/models endpoint.
func (p *Provider) Models() []core.ModelInfo {
	return core.ModelsFromList(p.name, p.SupportedModels())
}

type cohereRequest struct {
	Model       string         `json:"model"`
	Messages    []core.Message `json:"messages"`
	Temperature *float64       `json:"temperature,omitempty"`
	MaxTokens   *int           `json:"max_tokens,omitempty"`
	Stream      bool           `json:"stream,omitempty"`
}

type cohereContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type cohereMessage struct {
	Role    string               `json:"role"`
	Content []cohereContentBlock `json:"content"`
}

type cohereBilledUnits struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type cohereTokens struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type cohereUsage struct {
	BilledUnits cohereBilledUnits `json:"billed_units"`
	Tokens      cohereTokens      `json:"tokens"`
}

type cohereResponse struct {
	ID           string        `json:"id"`
	Message      cohereMessage `json:"message"`
	Usage        cohereUsage   `json:"usage"`
	FinishReason string        `json:"finish_reason"`
}

type cohereErrorResponse struct {
	Message string `json:"message"`
}

// Complete sends a chat completion request to Cohere.
func (p *Provider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	cohReq := cohereRequest{
		Model:       req.Model,
		Messages:    req.Messages,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	}

	bodyReader, _, release, err := core.JSONBodyReader(cohReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	defer release()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v2/chat", bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
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
		var errResp cohereErrorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Message != "" {
			return nil, fmt.Errorf("cohere API error (%d): %s", httpResp.StatusCode, errResp.Message)
		}
		return nil, fmt.Errorf("cohere API error (%d): %s", httpResp.StatusCode, string(respBody))
	}

	var cohResp cohereResponse
	if err := json.Unmarshal(respBody, &cohResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	var contentParts []string
	for _, block := range cohResp.Message.Content {
		if block.Type == "text" {
			contentParts = append(contentParts, block.Text)
		}
	}

	tokens := cohResp.Usage.Tokens
	return &core.Response{
		ID:    cohResp.ID,
		Model: req.Model,
		Choices: []core.Choice{
			{
				Index: 0,
				Message: core.Message{
					Role:    cohResp.Message.Role,
					Content: strings.Join(contentParts, ""),
				},
				FinishReason: cohResp.FinishReason,
			},
		},
		Usage: core.Usage{
			PromptTokens:     tokens.InputTokens,
			CompletionTokens: tokens.OutputTokens,
			TotalTokens:      tokens.InputTokens + tokens.OutputTokens,
		},
	}, nil
}

type cohereStreamEvent struct {
	Type  string          `json:"type"`
	Delta json.RawMessage `json:"delta"`
}

type cohereContentDelta struct {
	Message struct {
		Content struct {
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
}

type cohereMessageEndDelta struct {
	FinishReason string      `json:"finish_reason"`
	Usage        cohereUsage `json:"usage"`
}

// CompleteStream sends a streaming chat completion request to Cohere.
func (p *Provider) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
	cohReq := cohereRequest{
		Model:       req.Model,
		Messages:    req.Messages,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		Stream:      true,
	}

	bodyReader, _, release, err := core.JSONBodyReader(cohReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	defer release()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v2/chat", bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		defer func() { _ = httpResp.Body.Close() }()
		respBody, _ := io.ReadAll(httpResp.Body)
		var errResp cohereErrorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Message != "" {
			return nil, fmt.Errorf("cohere API error (%d): %s", httpResp.StatusCode, errResp.Message)
		}
		return nil, fmt.Errorf("cohere API error (%d): %s", httpResp.StatusCode, string(respBody))
	}

	ch := make(chan core.StreamChunk)
	go func() {
		defer close(ch)
		defer func() { _ = httpResp.Body.Close() }()

		scanner := bufio.NewScanner(httpResp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")

			var event cohereStreamEvent
			if json.Unmarshal([]byte(data), &event) != nil {
				continue
			}

			switch event.Type {
			case "content-delta":
				var delta cohereContentDelta
				if json.Unmarshal(event.Delta, &delta) != nil {
					continue
				}
				ch <- core.StreamChunk{
					Choices: []core.StreamChoice{
						{
							Index: 0,
							Delta: core.MessageDelta{
								Content: delta.Message.Content.Text,
							},
						},
					},
				}
			case "message-end":
				var delta cohereMessageEndDelta
				if json.Unmarshal(event.Delta, &delta) != nil {
					continue
				}
				ch <- core.StreamChunk{
					Choices: []core.StreamChoice{
						{
							Index:        0,
							FinishReason: delta.FinishReason,
						},
					},
				}
				return
			}
		}
		if err := scanner.Err(); err != nil {
			ch <- core.StreamChunk{Error: err}
		}
	}()

	return ch, nil
}

type cohereEmbedRequest struct {
	Texts     []string `json:"texts"`
	Model     string   `json:"model"`
	InputType string   `json:"input_type"`
}

type cohereEmbedResponse struct {
	ID         string      `json:"id"`
	Embeddings [][]float64 `json:"embeddings"`
	Texts      []string    `json:"texts"`
	Meta       struct {
		BilledUnits struct {
			InputTokens int `json:"input_tokens"`
		} `json:"billed_units"`
	} `json:"meta"`
}

// Embed sends an embedding request to Cohere's /v1/embed endpoint.
func (p *Provider) Embed(ctx context.Context, req core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	var texts []string
	switch v := req.Input.(type) {
	case string:
		texts = []string{v}
	case []string:
		texts = v
	case []interface{}:
		for i, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("unsupported input type at input[%d]: %T; expected string", i, item)
			}
			texts = append(texts, s)
		}
	default:
		return nil, fmt.Errorf("unsupported input type: %T", req.Input)
	}
	if len(texts) == 0 {
		return nil, fmt.Errorf("embedding input must contain at least one text")
	}
	switch req.EncodingFormat {
	case "", "float":
	default:
		return nil, fmt.Errorf("embed: unsupported encoding_format %q; Cohere embeddings return float vectors", req.EncodingFormat)
	}
	if req.Dimensions != nil {
		return nil, fmt.Errorf("embed: dimensions are not supported by Cohere embeddings")
	}
	if req.User != "" {
		return nil, fmt.Errorf("embed: user is not supported by Cohere embeddings")
	}

	cohReq := cohereEmbedRequest{
		Texts:     texts,
		Model:     req.Model,
		InputType: "search_document",
	}

	bodyReader, _, release, err := core.JSONBodyReader(cohReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal embed request: %w", err)
	}
	defer release()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/embed", bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create embed request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("embed request failed: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read embed response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		var errResp cohereErrorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Message != "" {
			return nil, fmt.Errorf("cohere embed API error (%d): %s", httpResp.StatusCode, errResp.Message)
		}
		return nil, fmt.Errorf("cohere embed API error (%d): %s", httpResp.StatusCode, string(respBody))
	}

	var cohResp cohereEmbedResponse
	if err := json.Unmarshal(respBody, &cohResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal embed response: %w", err)
	}

	data := make([]core.Embedding, len(cohResp.Embeddings))
	for i, emb := range cohResp.Embeddings {
		data[i] = core.Embedding{
			Object:    "embedding",
			Embedding: emb,
			Index:     i,
		}
	}

	return &core.EmbeddingResponse{
		Object: "list",
		Data:   data,
		Model:  req.Model,
		Usage: core.EmbeddingUsage{
			PromptTokens: cohResp.Meta.BilledUnits.InputTokens,
			TotalTokens:  cohResp.Meta.BilledUnits.InputTokens,
		},
	}, nil
}
