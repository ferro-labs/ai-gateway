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

// XAIProvider implements the Provider interface for xAI.
type XAIProvider struct {
	Base
	httpClient *http.Client
}

// NewXAI creates a new xAI provider.
func NewXAI(apiKey, baseURL string) (*XAIProvider, error) {
	if baseURL == "" {
		baseURL = "https://api.x.ai/v1"
	}
	baseURL = strings.TrimRight(baseURL, "/")

	return &XAIProvider{
		Base:       Base{name: "xai", apiKey: apiKey, baseURL: baseURL},
		httpClient: &http.Client{},
	}, nil
}

// AuthHeaders implements ProxiableProvider.
func (p *XAIProvider) AuthHeaders() map[string]string {
	return map[string]string{"Authorization": "Bearer " + p.apiKey}
}

// SupportedModels returns known xAI models.
func (p *XAIProvider) SupportedModels() []string {
	return []string{
		"grok-2-latest",
		"grok-2-vision-latest",
		"grok-beta",
	}
}

// SupportsModel returns true for Grok/xAI model names.
func (p *XAIProvider) SupportsModel(model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(normalized, "grok") || strings.HasPrefix(normalized, "xai")
}

// Models returns structured model metadata.
func (p *XAIProvider) Models() []ModelInfo {
	return ModelsFromList(p.name, p.SupportedModels())
}

// DiscoverModels fetches the live model list from the xAI /models endpoint.
func (p *XAIProvider) DiscoverModels(ctx context.Context) ([]ModelInfo, error) {
	return discoverOpenAICompatibleModels(ctx, p.httpClient, p.baseURL+"/models", p.apiKey, p.name)
}

type xaiRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature *float64  `json:"temperature,omitempty"`
	MaxTokens   *int      `json:"max_tokens,omitempty"`
	Stream      bool      `json:"stream,omitempty"`
}

type xaiResponse struct {
	ID      string   `json:"id"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

type xaiError struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// Complete sends a chat completion request to xAI.
func (p *XAIProvider) Complete(ctx context.Context, req Request) (*Response, error) {
	xReq := xaiRequest{
		Model:       req.Model,
		Messages:    req.Messages,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	}

	body, err := json.Marshal(xReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
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
		var errResp xaiError
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return nil, fmt.Errorf("xai API error (%d): %s", httpResp.StatusCode, errResp.Error.Message)
		}
		return nil, fmt.Errorf("xai API error (%d): %s", httpResp.StatusCode, string(respBody))
	}

	var xResp xaiResponse
	if err := json.Unmarshal(respBody, &xResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &Response{
		ID:       xResp.ID,
		Model:    xResp.Model,
		Provider: p.name,
		Choices:  xResp.Choices,
		Usage:    xResp.Usage,
	}, nil
}

type xaiStreamResponse struct {
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

// CompleteStream sends a streaming chat completion request to xAI.
func (p *XAIProvider) CompleteStream(ctx context.Context, req Request) (<-chan StreamChunk, error) {
	xReq := xaiRequest{
		Model:       req.Model,
		Messages:    req.Messages,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		Stream:      true,
	}

	body, err := json.Marshal(xReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
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
		var errResp xaiError
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return nil, fmt.Errorf("xai API error (%d): %s", httpResp.StatusCode, errResp.Error.Message)
		}
		return nil, fmt.Errorf("xai API error (%d): %s", httpResp.StatusCode, string(respBody))
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

			var chunk xaiStreamResponse
			if json.Unmarshal([]byte(data), &chunk) != nil {
				continue
			}

			sc := StreamChunk{ID: chunk.ID, Model: chunk.Model}
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
