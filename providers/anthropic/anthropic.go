// Package anthropic provides a client for the Anthropic API (Claude models).
package anthropic

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
const Name = "anthropic"

const defaultBaseURL = "https://api.anthropic.com"

// Provider implements the Anthropic API client.
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

// New creates a new Anthropic provider.
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
	return map[string]string{
		"x-api-key":         p.apiKey,
		"anthropic-version": "2023-06-01",
	}
}

// SupportedModels returns the list of models supported by this provider.
func (p *Provider) SupportedModels() []string {
	return []string{
		"claude-sonnet-4-20250514",
		"claude-3-5-sonnet-20241022",
		"claude-3-haiku-20240307",
		"claude-3-opus-20240229",
	}
}

// SupportsModel returns true if the model matches the Anthropic prefix.
func (p *Provider) SupportsModel(model string) bool {
	return strings.HasPrefix(model, "claude-")
}

// Models returns model information for all supported models.
func (p *Provider) Models() []core.ModelInfo {
	return core.ModelsFromList(p.name, p.SupportedModels())
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicRequest struct {
	Model       string             `json:"model"`
	MaxTokens   int                `json:"max_tokens"`
	System      string             `json:"system,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
	Temperature *float64           `json:"temperature,omitempty"`
	Stream      bool               `json:"stream,omitempty"`
}

type anthropicContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

type anthropicResponse struct {
	ID      string                  `json:"id"`
	Type    string                  `json:"type"`
	Role    string                  `json:"role"`
	Content []anthropicContentBlock `json:"content"`
	Model   string                  `json:"model"`
	Usage   anthropicUsage          `json:"usage"`
}

type anthropicError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type anthropicErrorResponse struct {
	Type  string         `json:"type"`
	Error anthropicError `json:"error"`
}

func buildMessages(req core.Request) ([]anthropicMessage, string) {
	var systemParts []string
	var messages []anthropicMessage
	for _, msg := range req.Messages {
		if msg.Role == "system" {
			systemParts = append(systemParts, msg.Content)
		} else {
			messages = append(messages, anthropicMessage{
				Role:    msg.Role,
				Content: msg.Content,
			})
		}
	}
	return messages, strings.Join(systemParts, "\n")
}

// Complete sends a chat completion request to Anthropic.
func (p *Provider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	messages, system := buildMessages(req)

	maxTokens := 1024
	if req.MaxTokens != nil {
		maxTokens = *req.MaxTokens
	}

	aReq := anthropicRequest{
		Model:       req.Model,
		MaxTokens:   maxTokens,
		Messages:    messages,
		Temperature: req.Temperature,
		System:      system,
	}

	bodyReader, _, release, err := core.JSONBodyReader(aReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	defer release()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/messages", bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	httpReq.Header.Set("content-type", "application/json")

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
		var errResp anthropicErrorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return nil, fmt.Errorf("anthropic API error (%d): %s", httpResp.StatusCode, errResp.Error.Message)
		}
		return nil, fmt.Errorf("anthropic API error (%d): %s", httpResp.StatusCode, string(respBody))
	}

	var aResp anthropicResponse
	if err := json.Unmarshal(respBody, &aResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	var content strings.Builder
	for _, block := range aResp.Content {
		if block.Type == "text" {
			content.WriteString(block.Text)
		}
	}

	totalTokens := aResp.Usage.InputTokens + aResp.Usage.OutputTokens

	return &core.Response{
		ID:    aResp.ID,
		Model: aResp.Model,
		Choices: []core.Choice{
			{
				Index: 0,
				Message: core.Message{
					Role:    aResp.Role,
					Content: content.String(),
				},
				FinishReason: "stop",
			},
		},
		Usage: core.Usage{
			PromptTokens:     aResp.Usage.InputTokens,
			CompletionTokens: aResp.Usage.OutputTokens,
			TotalTokens:      totalTokens,
			CacheReadTokens:  aResp.Usage.CacheReadInputTokens,
			CacheWriteTokens: aResp.Usage.CacheCreationInputTokens,
		},
	}, nil
}

type anthropicStreamMessageStart struct {
	Type    string `json:"type"`
	Message struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Role  string `json:"role"`
	} `json:"message"`
}

type anthropicStreamContentDelta struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
	Delta struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"delta"`
}

// CompleteStream sends a streaming chat completion request to Anthropic.
func (p *Provider) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
	messages, system := buildMessages(req)

	maxTokens := 1024
	if req.MaxTokens != nil {
		maxTokens = *req.MaxTokens
	}

	aReq := anthropicRequest{
		Model:       req.Model,
		MaxTokens:   maxTokens,
		Messages:    messages,
		Temperature: req.Temperature,
		System:      system,
		Stream:      true,
	}

	bodyReader, _, release, err := core.JSONBodyReader(aReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	defer release()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/messages", bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	httpReq.Header.Set("content-type", "application/json")

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		defer func() { _ = httpResp.Body.Close() }()
		respBody, _ := io.ReadAll(httpResp.Body)
		var errResp anthropicErrorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return nil, fmt.Errorf("anthropic API error (%d): %s", httpResp.StatusCode, errResp.Error.Message)
		}
		return nil, fmt.Errorf("anthropic API error (%d): %s", httpResp.StatusCode, string(respBody))
	}

	ch := make(chan core.StreamChunk)
	go func() {
		defer close(ch)
		defer func() { _ = httpResp.Body.Close() }()

		var msgID, model string
		scanner := bufio.NewScanner(httpResp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")

			var raw map[string]interface{}
			if json.Unmarshal([]byte(data), &raw) != nil {
				continue
			}

			eventType, _ := raw["type"].(string)
			switch eventType {
			case "message_start":
				var evt anthropicStreamMessageStart
				if json.Unmarshal([]byte(data), &evt) == nil {
					msgID = evt.Message.ID
					model = evt.Message.Model
				}
			case "content_block_delta":
				var evt anthropicStreamContentDelta
				if json.Unmarshal([]byte(data), &evt) == nil {
					ch <- core.StreamChunk{
						ID:    msgID,
						Model: model,
						Choices: []core.StreamChoice{
							{
								Index: evt.Index,
								Delta: core.MessageDelta{
									Content: evt.Delta.Text,
								},
							},
						},
					}
				}
			case "message_delta":
				ch <- core.StreamChunk{
					ID:    msgID,
					Model: model,
					Choices: []core.StreamChoice{
						{
							Index:        0,
							FinishReason: "stop",
						},
					},
				}
			}
		}
		if err := scanner.Err(); err != nil {
			ch <- core.StreamChunk{Error: err}
		}
	}()

	return ch, nil
}
