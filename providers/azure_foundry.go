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

// AzureFoundryProvider implements the Provider interface for Azure AI Foundry.
type AzureFoundryProvider struct {
	Base
	httpClient *http.Client
	apiVersion string
}

// NewAzureFoundry creates a new Azure AI Foundry provider.
func NewAzureFoundry(apiKey string, baseURL string, apiVersion string) (*AzureFoundryProvider, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("baseURL is required for azure-foundry provider")
	}
	if apiVersion == "" {
		apiVersion = "2024-05-01-preview"
	}

	return &AzureFoundryProvider{
		Base:       Base{name: "azure-foundry", apiKey: apiKey, baseURL: baseURL},
		httpClient: &http.Client{},
		apiVersion: apiVersion,
	}, nil
}

// AuthHeaders implements ProxiableProvider.
func (p *AzureFoundryProvider) AuthHeaders() map[string]string {
	return map[string]string{"api-key": p.apiKey}
}

// SupportedModels returns known Azure Foundry models.
func (p *AzureFoundryProvider) SupportedModels() []string {
	return []string{
		"gpt-4o",
		"gpt-4.1",
		"phi-4",
	}
}

// SupportsModel returns true for any model — Azure Foundry validates model names.
func (p *AzureFoundryProvider) SupportsModel(_ string) bool {
	return true
}

// Models returns structured model metadata.
func (p *AzureFoundryProvider) Models() []ModelInfo {
	return ModelsFromList(p.name, p.SupportedModels())
}

type azureFoundryRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature *float64  `json:"temperature,omitempty"`
	MaxTokens   *int      `json:"max_tokens,omitempty"`
	Stream      bool      `json:"stream,omitempty"`
}

type azureFoundryResponse struct {
	ID      string   `json:"id"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

type azureFoundryErrorResponse struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

func (p *AzureFoundryProvider) endpoint() string {
	return fmt.Sprintf("%s/chat/completions?api-version=%s", p.baseURL, p.apiVersion)
}

// Complete sends a chat completion request and returns the full response.
func (p *AzureFoundryProvider) Complete(ctx context.Context, req Request) (*Response, error) {
	foundryReq := azureFoundryRequest{
		Model:       req.Model,
		Messages:    req.Messages,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	}

	body, err := json.Marshal(foundryReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint(), bytes.NewReader(body))
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
		var errResp azureFoundryErrorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return nil, fmt.Errorf("azure foundry API error (%d): %s", httpResp.StatusCode, errResp.Error.Message)
		}
		return nil, fmt.Errorf("azure foundry API error (%d): %s", httpResp.StatusCode, string(respBody))
	}

	var foundryResp azureFoundryResponse
	if err := json.Unmarshal(respBody, &foundryResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &Response{
		ID:       foundryResp.ID,
		Model:    foundryResp.Model,
		Provider: p.name,
		Choices:  foundryResp.Choices,
		Usage:    foundryResp.Usage,
	}, nil
}

type azureFoundryStreamResponse struct {
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

// CompleteStream sends a streaming chat completion request to Azure AI Foundry.
func (p *AzureFoundryProvider) CompleteStream(ctx context.Context, req Request) (<-chan StreamChunk, error) {
	foundryReq := azureFoundryRequest{
		Model:       req.Model,
		Messages:    req.Messages,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		Stream:      true,
	}

	body, err := json.Marshal(foundryReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("api-key", p.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		defer func() { _ = httpResp.Body.Close() }()
		respBody, _ := io.ReadAll(httpResp.Body)
		var errResp azureFoundryErrorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return nil, fmt.Errorf("azure foundry API error (%d): %s", httpResp.StatusCode, errResp.Error.Message)
		}
		return nil, fmt.Errorf("azure foundry API error (%d): %s", httpResp.StatusCode, string(respBody))
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

			var chunk azureFoundryStreamResponse
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
