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

// AzureOpenAIProvider implements the Provider interface for Azure OpenAI.
type AzureOpenAIProvider struct {
	Base
	httpClient     *http.Client
	deploymentName string
	apiVersion     string
}

// NewAzureOpenAI creates a new Azure OpenAI provider.
func NewAzureOpenAI(apiKey string, baseURL string, deploymentName string, apiVersion string) (*AzureOpenAIProvider, error) {
	baseURL = strings.TrimRight(baseURL, "/")
	if apiVersion == "" {
		apiVersion = "2024-10-21"
	}

	return &AzureOpenAIProvider{
		Base:           Base{name: "azure-openai", apiKey: apiKey, baseURL: baseURL},
		httpClient:     &http.Client{},
		deploymentName: deploymentName,
		apiVersion:     apiVersion,
	}, nil
}

// AuthHeaders implements ProxiableProvider.
func (p *AzureOpenAIProvider) AuthHeaders() map[string]string {
	return map[string]string{"api-key": p.apiKey}
}

// SupportedModels returns the static list of known models for the /v1/models endpoint.
func (p *AzureOpenAIProvider) SupportedModels() []string {
	return []string{p.deploymentName}
}

// SupportsModel returns true for any model — the upstream provider validates model names.
func (p *AzureOpenAIProvider) SupportsModel(_ string) bool {
	return true
}

// Models returns structured model metadata for the /v1/models endpoint.
func (p *AzureOpenAIProvider) Models() []ModelInfo {
	return []ModelInfo{
		{
			ID:      p.deploymentName,
			Object:  "model",
			OwnedBy: p.name,
		},
	}
}

type azureOpenAIRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature *float64  `json:"temperature,omitempty"`
	MaxTokens   *int      `json:"max_tokens,omitempty"`
	Stream      bool      `json:"stream,omitempty"`
}

type azureOpenAIResponse struct {
	ID      string   `json:"id"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

type azureOpenAIErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

type azureOpenAIErrorResponse struct {
	Error azureOpenAIErrorDetail `json:"error"`
}

func (p *AzureOpenAIProvider) endpoint() string {
	return fmt.Sprintf("%s/openai/deployments/%s/chat/completions?api-version=%s", p.baseURL, p.deploymentName, p.apiVersion)
}

// Complete sends a chat completion request and returns the full response.
func (p *AzureOpenAIProvider) Complete(ctx context.Context, req Request) (*Response, error) {
	azureReq := azureOpenAIRequest{
		Model:       req.Model,
		Messages:    req.Messages,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	}

	body, err := json.Marshal(azureReq)
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
		var errResp azureOpenAIErrorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return nil, fmt.Errorf("azure openai API error (%d): %s", httpResp.StatusCode, errResp.Error.Message)
		}
		return nil, fmt.Errorf("azure openai API error (%d): %s", httpResp.StatusCode, string(respBody))
	}

	var azureResp azureOpenAIResponse
	if err := json.Unmarshal(respBody, &azureResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &Response{
		ID:      azureResp.ID,
		Model:   azureResp.Model,
		Choices: azureResp.Choices,
		Usage:   azureResp.Usage,
	}, nil
}

type azureOpenAIStreamResponse struct {
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

// CompleteStream sends a streaming chat completion request to Azure OpenAI.
func (p *AzureOpenAIProvider) CompleteStream(ctx context.Context, req Request) (<-chan StreamChunk, error) {
	azureReq := azureOpenAIRequest{
		Model:       req.Model,
		Messages:    req.Messages,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		Stream:      true,
	}

	body, err := json.Marshal(azureReq)
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
		var errResp azureOpenAIErrorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return nil, fmt.Errorf("azure openai API error (%d): %s", httpResp.StatusCode, errResp.Error.Message)
		}
		return nil, fmt.Errorf("azure openai API error (%d): %s", httpResp.StatusCode, string(respBody))
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

			var chunk azureOpenAIStreamResponse
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
