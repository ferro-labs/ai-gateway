package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// MistralProvider implements the Provider interface for Mistral AI.
type MistralProvider struct {
	httpClient *http.Client
	apiKey     string
	baseURL    string
	name       string
}

// NewMistral creates a new Mistral AI provider.
func NewMistral(apiKey string, baseURL string) (*MistralProvider, error) {
	if baseURL == "" {
		baseURL = "https://api.mistral.ai"
	}
	baseURL = strings.TrimRight(baseURL, "/")

	return &MistralProvider{
		httpClient: &http.Client{},
		apiKey:     apiKey,
		baseURL:    baseURL,
		name:       "mistral",
	}, nil
}

// Name returns the provider identifier.
func (p *MistralProvider) Name() string { return p.name }

// BaseURL implements ProxiableProvider.
func (p *MistralProvider) BaseURL() string { return p.baseURL }

// AuthHeaders implements ProxiableProvider.
func (p *MistralProvider) AuthHeaders() map[string]string {
	return map[string]string{"Authorization": "Bearer " + p.apiKey}
}

// SupportedModels returns the static list of known models for the /v1/models endpoint.
func (p *MistralProvider) SupportedModels() []string {
	return []string{
		"mistral-large-latest",
		"mistral-small-latest",
		"open-mistral-nemo",
		"codestral-latest",
	}
}

// SupportsModel returns true if the model matches a known Mistral prefix.
func (p *MistralProvider) SupportsModel(model string) bool {
	for _, prefix := range []string{"mistral-", "codestral-", "open-mistral-", "pixtral-", "ministral-"} {
		if strings.HasPrefix(model, prefix) {
			return true
		}
	}
	return false
}

// Models returns structured model metadata for the /v1/models endpoint.
func (p *MistralProvider) Models() []ModelInfo {
	supported := p.SupportedModels()
	models := make([]ModelInfo, len(supported))
	for i, id := range supported {
		models[i] = ModelInfo{
			ID:      id,
			Object:  "model",
			OwnedBy: p.name,
		}
	}
	return models
}

// mistralRequest is OpenAI-compatible.
type mistralRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature *float64  `json:"temperature,omitempty"`
	MaxTokens   *int      `json:"max_tokens,omitempty"`
	Stream      bool      `json:"stream,omitempty"`
}

type mistralResponse struct {
	ID      string   `json:"id"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

type mistralErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

type mistralErrorResponse struct {
	Error mistralErrorDetail `json:"error"`
}

// Complete sends a chat completion request and returns the full response.
func (p *MistralProvider) Complete(ctx context.Context, req Request) (*Response, error) {
	mistralReq := mistralRequest{
		Model:       req.Model,
		Messages:    req.Messages,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	}

	body, err := json.Marshal(mistralReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/chat/completions", bytes.NewReader(body))
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
		var errResp mistralErrorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return nil, fmt.Errorf("mistral API error (%d): %s", httpResp.StatusCode, errResp.Error.Message)
		}
		return nil, fmt.Errorf("mistral API error (%d): %s", httpResp.StatusCode, string(respBody))
	}

	var mistralResp mistralResponse
	if err := json.Unmarshal(respBody, &mistralResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &Response{
		ID:      mistralResp.ID,
		Model:   mistralResp.Model,
		Choices: mistralResp.Choices,
		Usage:   mistralResp.Usage,
	}, nil
}

type mistralStreamResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Role    string `json:"role,omitempty"`
			Content string `json:"content,omitempty"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason,omitempty"`
	} `json:"choices"`
}

// CompleteStream sends a streaming chat completion request to Mistral AI.
func (p *MistralProvider) CompleteStream(ctx context.Context, req Request) (<-chan StreamChunk, error) {
	mistralReq := mistralRequest{
		Model:       req.Model,
		Messages:    req.Messages,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		Stream:      true,
	}

	body, err := json.Marshal(mistralReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/chat/completions", bytes.NewReader(body))
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
		var errResp mistralErrorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return nil, fmt.Errorf("mistral API error (%d): %s", httpResp.StatusCode, errResp.Error.Message)
		}
		return nil, fmt.Errorf("mistral API error (%d): %s", httpResp.StatusCode, string(respBody))
	}

	ch := make(chan StreamChunk)
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
			if data == SSEDone {
				return
			}

			var chunk mistralStreamResponse
			if json.Unmarshal([]byte(data), &chunk) != nil {
				continue
			}

			sc := StreamChunk{
				ID:    chunk.ID,
				Model: chunk.Model,
			}
			for _, c := range chunk.Choices {
				sc.Choices = append(sc.Choices, StreamChoice{
					Index: c.Index,
					Delta: MessageDelta{
						Role:    c.Delta.Role,
						Content: c.Delta.Content,
					},
					FinishReason: c.FinishReason,
				})
			}
			ch <- sc
		}
		if err := scanner.Err(); err != nil {
			ch <- StreamChunk{Error: err}
		}
	}()

	return ch, nil
}
