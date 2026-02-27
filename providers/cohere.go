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

// CohereProvider implements the Provider interface for Cohere.
type CohereProvider struct {
	Base
	httpClient *http.Client
}

// NewCohere creates a new Cohere provider.
func NewCohere(apiKey string, baseURL string) (*CohereProvider, error) {
	if baseURL == "" {
		baseURL = "https://api.cohere.com"
	}
	baseURL = strings.TrimRight(baseURL, "/")

	return &CohereProvider{
		Base:       Base{name: "cohere", apiKey: apiKey, baseURL: baseURL},
		httpClient: &http.Client{},
	}, nil
}

// AuthHeaders implements ProxiableProvider.
func (p *CohereProvider) AuthHeaders() map[string]string {
	return map[string]string{"Authorization": "Bearer " + p.apiKey}
}

// SupportedModels returns the static list of known models for the /v1/models endpoint.
func (p *CohereProvider) SupportedModels() []string {
	return []string{
		"command-r-plus",
		"command-r",
		"command-light",
		"command",
	}
}

// SupportsModel returns true if the model matches a known Cohere prefix.
func (p *CohereProvider) SupportsModel(model string) bool {
	for _, prefix := range []string{"command", "embed-", "rerank-"} {
		if strings.HasPrefix(model, prefix) {
			return true
		}
	}
	return false
}

// Models returns structured model metadata for the /v1/models endpoint.
func (p *CohereProvider) Models() []ModelInfo {
	return ModelsFromList(p.name, p.SupportedModels())
}

type cohereRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature *float64  `json:"temperature,omitempty"`
	MaxTokens   *int      `json:"max_tokens,omitempty"`
	Stream      bool      `json:"stream,omitempty"`
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

// Complete sends a chat completion request and returns the full response.
func (p *CohereProvider) Complete(ctx context.Context, req Request) (*Response, error) {
	cohereReq := cohereRequest{
		Model:       req.Model,
		Messages:    req.Messages,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	}

	body, err := json.Marshal(cohereReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v2/chat", bytes.NewReader(body))
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

	var cohereResp cohereResponse
	if err := json.Unmarshal(respBody, &cohereResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	var contentParts []string
	for _, block := range cohereResp.Message.Content {
		if block.Type == "text" {
			contentParts = append(contentParts, block.Text)
		}
	}

	tokens := cohereResp.Usage.Tokens
	return &Response{
		ID:    cohereResp.ID,
		Model: req.Model,
		Choices: []Choice{
			{
				Index: 0,
				Message: Message{
					Role:    cohereResp.Message.Role,
					Content: strings.Join(contentParts, ""),
				},
				FinishReason: cohereResp.FinishReason,
			},
		},
		Usage: Usage{
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
func (p *CohereProvider) CompleteStream(ctx context.Context, req Request) (<-chan StreamChunk, error) {
	cohereReq := cohereRequest{
		Model:       req.Model,
		Messages:    req.Messages,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		Stream:      true,
	}

	body, err := json.Marshal(cohereReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v2/chat", bytes.NewReader(body))
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
				ch <- StreamChunk{
					Choices: []StreamChoice{
						{
							Index: 0,
							Delta: MessageDelta{
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
				ch <- StreamChunk{
					Choices: []StreamChoice{
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
			ch <- StreamChunk{Error: err}
		}
	}()

	return ch, nil
}
