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

// FireworksProvider implements the Provider interface for Fireworks AI.
type FireworksProvider struct {
	Base
	httpClient *http.Client
}

// NewFireworks creates a new Fireworks AI provider.
func NewFireworks(apiKey, baseURL string) (*FireworksProvider, error) {
	if baseURL == "" {
		baseURL = "https://api.fireworks.ai/inference"
	}
	baseURL = strings.TrimRight(baseURL, "/")
	return &FireworksProvider{
		Base:       Base{name: "fireworks", apiKey: apiKey, baseURL: baseURL},
		httpClient: &http.Client{},
	}, nil
}

// AuthHeaders implements ProxiableProvider.
func (p *FireworksProvider) AuthHeaders() map[string]string {
	return map[string]string{"Authorization": "Bearer " + p.apiKey}
}

// SupportedModels returns known Fireworks AI models.
func (p *FireworksProvider) SupportedModels() []string {
	return []string{
		"accounts/fireworks/models/llama-v3p1-8b-instruct",
		"accounts/fireworks/models/llama-v3p1-70b-instruct",
		"accounts/fireworks/models/llama-v3p1-405b-instruct",
		"accounts/fireworks/models/llama-v3p2-3b-instruct",
		"accounts/fireworks/models/llama-v3p2-11b-vision-instruct",
		"accounts/fireworks/models/mixtral-8x7b-instruct",
		"accounts/fireworks/models/mixtral-8x22b-instruct",
		"accounts/fireworks/models/firefunction-v2",
		"accounts/fireworks/models/qwen2p5-72b-instruct",
		"accounts/fireworks/models/deepseek-v3",
	}
}

// SupportsModel returns true for any model — Fireworks validates model names.
func (p *FireworksProvider) SupportsModel(_ string) bool {
	return true
}

// Models returns structured model metadata.
func (p *FireworksProvider) Models() []ModelInfo {
	return ModelsFromList(p.name, p.SupportedModels())
}

// DiscoverModels fetches the live model list from the Fireworks AI /v1/models endpoint.
func (p *FireworksProvider) DiscoverModels(ctx context.Context) ([]ModelInfo, error) {
	return discoverOpenAICompatibleModels(ctx, p.httpClient, p.baseURL+"/v1/models", p.apiKey, p.name)
}

type fireworksRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature *float64  `json:"temperature,omitempty"`
	MaxTokens   *int      `json:"max_tokens,omitempty"`
	Stream      bool      `json:"stream,omitempty"`
}

type fireworksResponse struct {
	ID      string   `json:"id"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

type fireworksError struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// Complete sends a chat completion request to Fireworks AI.
func (p *FireworksProvider) Complete(ctx context.Context, req Request) (*Response, error) {
	fReq := fireworksRequest{
		Model:       req.Model,
		Messages:    req.Messages,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	}

	body, err := json.Marshal(fReq)
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
		var errResp fireworksError
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return nil, fmt.Errorf("fireworks API error (%d): %s", httpResp.StatusCode, errResp.Error.Message)
		}
		return nil, fmt.Errorf("fireworks API error (%d): %s", httpResp.StatusCode, string(respBody))
	}

	var fResp fireworksResponse
	if err := json.Unmarshal(respBody, &fResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &Response{
		ID:       fResp.ID,
		Model:    fResp.Model,
		Provider: p.name,
		Choices:  fResp.Choices,
		Usage:    fResp.Usage,
	}, nil
}

type fireworksStreamResponse struct {
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

// CompleteStream sends a streaming chat completion request to Fireworks AI.
func (p *FireworksProvider) CompleteStream(ctx context.Context, req Request) (<-chan StreamChunk, error) {
	fReq := fireworksRequest{
		Model:       req.Model,
		Messages:    req.Messages,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		Stream:      true,
	}

	body, err := json.Marshal(fReq)
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
		var errResp fireworksError
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return nil, fmt.Errorf("fireworks API error (%d): %s", httpResp.StatusCode, errResp.Error.Message)
		}
		return nil, fmt.Errorf("fireworks API error (%d): %s", httpResp.StatusCode, string(respBody))
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

			var chunk fireworksStreamResponse
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
