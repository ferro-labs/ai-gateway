// Package azureopenai provides a client for the Azure OpenAI API.
package azureopenai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// Name is the canonical provider identifier.
const Name = "azure-openai"

const defaultAPIVersion = "2024-10-21"

// Provider implements the Azure OpenAI API client.
type Provider struct {
	name           string
	apiKey         string
	baseURL        string
	deploymentName string
	apiVersion     string
	httpClient     *http.Client
}

// Compile-time interface assertions.
var (
	_ core.Provider          = (*Provider)(nil)
	_ core.StreamProvider    = (*Provider)(nil)
	_ core.ProxiableProvider = (*Provider)(nil)
)

// New creates a new Azure OpenAI provider.
func New(apiKey, baseURL, deploymentName, apiVersion string) (*Provider, error) {
	baseURL = strings.TrimRight(baseURL, "/")
	if apiVersion == "" {
		apiVersion = defaultAPIVersion
	}
	return &Provider{
		name:           Name,
		apiKey:         apiKey,
		baseURL:        baseURL,
		deploymentName: deploymentName,
		apiVersion:     apiVersion,
		httpClient:     &http.Client{},
	}, nil
}

// Name implements core.Provider.
func (p *Provider) Name() string { return p.name }

// BaseURL implements core.ProxiableProvider.
func (p *Provider) BaseURL() string { return p.baseURL }

// APIVersion returns the configured Azure API version.
func (p *Provider) APIVersion() string { return p.apiVersion }

// AuthHeaders implements core.ProxiableProvider.
func (p *Provider) AuthHeaders() map[string]string {
	return map[string]string{"api-key": p.apiKey}
}

// SupportedModels returns the deployment name as the only supported model.
func (p *Provider) SupportedModels() []string {
	return []string{p.deploymentName}
}

// SupportsModel returns true for any model — the upstream provider validates model names.
func (p *Provider) SupportsModel(_ string) bool { return true }

// Models returns structured model metadata.
func (p *Provider) Models() []core.ModelInfo {
	return []core.ModelInfo{
		{
			ID:      p.deploymentName,
			Object:  "model",
			OwnedBy: p.name,
		},
	}
}

func (p *Provider) endpoint() string {
	return fmt.Sprintf("%s/openai/deployments/%s/chat/completions?api-version=%s",
		p.baseURL, p.deploymentName, p.apiVersion)
}

type azureOpenAIRequest struct {
	Model       string         `json:"model"`
	Messages    []core.Message `json:"messages"`
	Temperature *float64       `json:"temperature,omitempty"`
	MaxTokens   *int           `json:"max_tokens,omitempty"`
	Stream      bool           `json:"stream,omitempty"`
}

type azureOpenAIResponse struct {
	ID      string        `json:"id"`
	Model   string        `json:"model"`
	Choices []core.Choice `json:"choices"`
	Usage   core.Usage    `json:"usage"`
}

type azureOpenAIErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

type azureOpenAIErrorResponse struct {
	Error azureOpenAIErrorDetail `json:"error"`
}

// Complete sends a chat completion request to Azure OpenAI.
func (p *Provider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
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

	return &core.Response{
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
func (p *Provider) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
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
			if data == core.SSEDone {
				return
			}

			var chunk azureOpenAIStreamResponse
			if json.Unmarshal([]byte(data), &chunk) != nil {
				continue
			}

			sc := core.StreamChunk{
				ID:    chunk.ID,
				Model: chunk.Model,
			}
			for _, c := range chunk.Choices {
				sc.Choices = append(sc.Choices, core.StreamChoice{
					Index: c.Index,
					Delta: core.MessageDelta{
						Role:    c.Delta.Role,
						Content: c.Delta.Content,
					},
					FinishReason: c.FinishReason,
				})
			}
			ch <- sc
		}
		if err := scanner.Err(); err != nil {
			ch <- core.StreamChunk{Error: err}
		}
	}()

	return ch, nil
}
