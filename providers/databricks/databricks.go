// Package databricks provides a client for the Databricks model serving API.
package databricks

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

const (
	// Name is the canonical identifier for the Databricks provider.
	// Re-exported as providers.NameDatabricks in providers/names.go.
	Name = "databricks"
)

// Provider implements the core.Provider interface for Databricks.
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
)

// New creates a new Databricks provider.
//
// baseURL should point at the Databricks host or the OpenAI-compatible serving
// path. If a plain workspace host is provided, /serving-endpoints is appended.
func New(apiKey, baseURL string) (*Provider, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, fmt.Errorf("databricks: baseURL is required")
	}
	baseURL = normalizeBaseURL(baseURL)
	return &Provider{
		name:       Name,
		apiKey:     apiKey,
		baseURL:    baseURL,
		httpClient: providerhttp.ForProvider(Name),
	}, nil
}

func normalizeBaseURL(baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	baseURL = strings.TrimSuffix(baseURL, "/chat/completions")
	baseURL = strings.TrimSuffix(baseURL, "/models")
	if !strings.Contains(baseURL, "/serving-endpoints") {
		baseURL += "/serving-endpoints"
	}
	return baseURL
}

// Name implements core.Provider.
func (p *Provider) Name() string { return p.name }

// BaseURL implements core.ProxiableProvider.
func (p *Provider) BaseURL() string { return p.baseURL }

// AuthHeaders implements core.ProxiableProvider.
func (p *Provider) AuthHeaders() map[string]string {
	return map[string]string{"Authorization": "Bearer " + p.apiKey}
}

// SupportedModels returns known Databricks-hosted foundation model examples.
func (p *Provider) SupportedModels() []string {
	return []string{
		"databricks-claude-3-7-sonnet",
		"databricks-claude-sonnet-4-5",
		"databricks-gemini-2-5-pro",
		"databricks-gpt-oss-120b",
		"databricks-llama-4-maverick",
	}
}

// SupportsModel returns true for any Databricks serving endpoint name.
func (p *Provider) SupportsModel(_ string) bool { return true }

// Models returns structured model metadata.
func (p *Provider) Models() []core.ModelInfo {
	return core.ModelsFromList(p.name, p.SupportedModels())
}

type request struct {
	Model       string         `json:"model"`
	Messages    []core.Message `json:"messages"`
	Temperature *float64       `json:"temperature,omitempty"`
	MaxTokens   *int           `json:"max_tokens,omitempty"`
	Stream      bool           `json:"stream,omitempty"`
}

type response struct {
	ID      string        `json:"id"`
	Model   string        `json:"model"`
	Choices []core.Choice `json:"choices"`
	Usage   core.Usage    `json:"usage"`
}

type errorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

type errorResponse struct {
	Error errorDetail `json:"error"`
}

type streamResponse struct {
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

// Complete sends a chat completion request to Databricks.
func (p *Provider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	pReq := request{
		Model:       req.Model,
		Messages:    req.Messages,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	}

	bodyReader, _, release, err := core.JSONBodyReader(pReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	defer release()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bodyReader)
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
		var errResp errorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return nil, fmt.Errorf("databricks API error (%d): %s", httpResp.StatusCode, errResp.Error.Message)
		}
		return nil, fmt.Errorf("databricks API error (%d): %s", httpResp.StatusCode, string(respBody))
	}

	var pResp response
	if err := json.Unmarshal(respBody, &pResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &core.Response{
		ID:       pResp.ID,
		Model:    pResp.Model,
		Provider: p.name,
		Choices:  pResp.Choices,
		Usage:    pResp.Usage,
	}, nil
}

// CompleteStream sends a streaming chat completion request to Databricks.
func (p *Provider) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
	pReq := request{
		Model:       req.Model,
		Messages:    req.Messages,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		Stream:      true,
	}

	bodyReader, _, release, err := core.JSONBodyReader(pReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	defer release()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bodyReader)
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
		var errResp errorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return nil, fmt.Errorf("databricks API error (%d): %s", httpResp.StatusCode, errResp.Error.Message)
		}
		return nil, fmt.Errorf("databricks API error (%d): %s", httpResp.StatusCode, string(respBody))
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

			var sr streamResponse
			if err := json.Unmarshal([]byte(data), &sr); err != nil {
				ch <- core.StreamChunk{Error: fmt.Errorf("failed to unmarshal stream chunk: %w", err)}
				return
			}

			choices := make([]core.StreamChoice, len(sr.Choices))
			for i, c := range sr.Choices {
				choices[i] = core.StreamChoice{
					Index:        c.Index,
					FinishReason: c.FinishReason,
					Delta: core.MessageDelta{
						Role:    c.Delta.Role,
						Content: c.Delta.Content,
					},
				}
			}

			ch <- core.StreamChunk{
				ID:      sr.ID,
				Model:   sr.Model,
				Choices: choices,
			}
		}
		if err := scanner.Err(); err != nil {
			ch <- core.StreamChunk{Error: fmt.Errorf("stream read error: %w", err)}
		}
	}()

	return ch, nil
}
