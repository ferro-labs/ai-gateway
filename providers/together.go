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

// TogetherProvider implements the Provider interface for Together AI.
type TogetherProvider struct {
	Base
	httpClient *http.Client
}

// NewTogether creates a new Together AI provider.
func NewTogether(apiKey string, baseURL string) (*TogetherProvider, error) {
	if baseURL == "" {
		baseURL = "https://api.together.xyz"
	}
	baseURL = strings.TrimRight(baseURL, "/")

	return &TogetherProvider{
		Base:       Base{name: "together", apiKey: apiKey, baseURL: baseURL},
		httpClient: &http.Client{},
	}, nil
}

// AuthHeaders implements ProxiableProvider.
func (p *TogetherProvider) AuthHeaders() map[string]string {
	return map[string]string{"Authorization": "Bearer " + p.apiKey}
}

// SupportedModels returns the static list of known models for the /v1/models endpoint.
func (p *TogetherProvider) SupportedModels() []string {
	return []string{
		"meta-llama/Meta-Llama-3.1-8B-Instruct-Turbo",
		"meta-llama/Meta-Llama-3.1-70B-Instruct-Turbo",
		"mistralai/Mixtral-8x7B-Instruct-v0.1",
		"Qwen/Qwen2.5-72B-Instruct-Turbo",
	}
}

// SupportsModel returns true for any model — the upstream provider validates model names.
func (p *TogetherProvider) SupportsModel(_ string) bool {
	return true
}

// Models returns structured model metadata for the /v1/models endpoint.
func (p *TogetherProvider) Models() []ModelInfo {
	return ModelsFromList(p.name, p.SupportedModels())
}

// togetherRequest is OpenAI-compatible.
type togetherRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature *float64  `json:"temperature,omitempty"`
	MaxTokens   *int      `json:"max_tokens,omitempty"`
	Stream      bool      `json:"stream,omitempty"`
}

type togetherResponse struct {
	ID      string   `json:"id"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

type togetherErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

type togetherErrorResponse struct {
	Error togetherErrorDetail `json:"error"`
}

// Complete sends a chat completion request and returns the full response.
func (p *TogetherProvider) Complete(ctx context.Context, req Request) (*Response, error) {
	togetherReq := togetherRequest{
		Model:       req.Model,
		Messages:    req.Messages,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	}

	body, err := json.Marshal(togetherReq)
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
		var errResp togetherErrorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return nil, fmt.Errorf("together API error (%d): %s", httpResp.StatusCode, errResp.Error.Message)
		}
		return nil, fmt.Errorf("together API error (%d): %s", httpResp.StatusCode, string(respBody))
	}

	var togetherResp togetherResponse
	if err := json.Unmarshal(respBody, &togetherResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &Response{
		ID:      togetherResp.ID,
		Model:   togetherResp.Model,
		Choices: togetherResp.Choices,
		Usage:   togetherResp.Usage,
	}, nil
}

type togetherStreamResponse struct {
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

// CompleteStream sends a streaming chat completion request to Together AI.
func (p *TogetherProvider) CompleteStream(ctx context.Context, req Request) (<-chan StreamChunk, error) {
	togetherReq := togetherRequest{
		Model:       req.Model,
		Messages:    req.Messages,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		Stream:      true,
	}

	body, err := json.Marshal(togetherReq)
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
		var errResp togetherErrorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return nil, fmt.Errorf("together API error (%d): %s", httpResp.StatusCode, errResp.Error.Message)
		}
		return nil, fmt.Errorf("together API error (%d): %s", httpResp.StatusCode, string(respBody))
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

			var chunk togetherStreamResponse
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
