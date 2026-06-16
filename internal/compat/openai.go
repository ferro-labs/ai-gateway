// Package compat provides a shared HTTP client for LLM providers that expose
// an OpenAI-compatible /chat/completions endpoint.
//
// Providers such as OpenRouter, NanoGPT, and Z.ai all use the same wire
// protocol: POST /chat/completions for completions, POST /chat/completions
// with "stream": true for SSE streaming, and GET /models for live discovery.
// The Client type here centralises that logic so individual provider packages
// need only supply their name, default URL, and static model list.
package compat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	discov "github.com/ferro-labs/ai-gateway/internal/discovery"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// Client handles the HTTP round-trips for an OpenAI-compatible chat
// completions API. Embed *Client in a provider struct; the promoted methods
// satisfy core.Provider, core.StreamProvider, core.ProxiableProvider, and
// core.DiscoveryProvider. The concrete provider only needs to add
// SupportedModels, SupportsModel, and Models.
type Client struct {
	name       string
	apiKey     string
	baseURL    string
	httpClient *http.Client
}

// New returns a Client. baseURL overrides defaultBaseURL when non-empty; the
// resulting BaseURL() has no trailing slash.
func New(name, apiKey, baseURL, defaultBaseURL string, httpClient *http.Client) *Client {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Client{
		name:       name,
		apiKey:     apiKey,
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: httpClient,
	}
}

// Name returns the provider's canonical identifier.
func (c *Client) Name() string { return c.name }

// BaseURL implements core.ProxiableProvider.
func (c *Client) BaseURL() string { return c.baseURL }

// AuthHeaders implements core.ProxiableProvider.
func (c *Client) AuthHeaders() map[string]string {
	return map[string]string{"Authorization": "Bearer " + c.apiKey}
}

// DiscoverModels fetches live model metadata from the provider's /models
// endpoint using the standard OpenAI model list format.
func (c *Client) DiscoverModels(ctx context.Context) ([]core.ModelInfo, error) {
	return discov.DiscoverOpenAICompatibleModels(ctx, c.httpClient, c.baseURL+"/models", c.apiKey, c.name)
}

// Complete sends a non-streaming chat completion request. All fields of
// core.Request are forwarded to the provider; optional fields are omitted
// when zero/nil.
func (c *Client) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	req.Stream = false

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		var errResp apiErrorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return nil, fmt.Errorf("%s API error (%d): %s", c.name, httpResp.StatusCode, errResp.Error.Message)
		}
		return nil, fmt.Errorf("%s API error (%d): %s", c.name, httpResp.StatusCode, string(respBody))
	}

	var resp chatResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &core.Response{
		ID:       resp.ID,
		Object:   resp.Object,
		Created:  resp.Created,
		Model:    resp.Model,
		Provider: c.name,
		Choices:  resp.Choices,
		Usage:    resp.Usage,
	}, nil
}

// CompleteStream sends a streaming chat completion request and returns a
// channel of SSE chunks. The channel is closed when the stream ends or an
// error occurs; errors are delivered as StreamChunk.Error.
func (c *Client) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
	req.Stream = true

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		defer func() { _ = httpResp.Body.Close() }()
		respBody, _ := io.ReadAll(httpResp.Body)
		var errResp apiErrorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return nil, fmt.Errorf("%s API error (%d): %s", c.name, httpResp.StatusCode, errResp.Error.Message)
		}
		return nil, fmt.Errorf("%s API error (%d): %s", c.name, httpResp.StatusCode, string(respBody))
	}

	ch := make(chan core.StreamChunk)
	go func() {
		defer close(ch)
		defer func() { _ = httpResp.Body.Close() }()

		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == core.SSEDone {
				return
			}

			var chunk sseChunk
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				ch <- core.StreamChunk{Error: fmt.Errorf("failed to unmarshal stream chunk: %w", err)}
				return
			}

			choices := make([]core.StreamChoice, len(chunk.Choices))
			for i, choice := range chunk.Choices {
				choices[i] = core.StreamChoice{
					Index:        choice.Index,
					FinishReason: choice.FinishReason,
					Delta: core.MessageDelta{
						Role:    choice.Delta.Role,
						Content: choice.Delta.Content,
					},
				}
			}

			ch <- core.StreamChunk{
				ID:      chunk.ID,
				Model:   chunk.Model,
				Choices: choices,
			}
		}
		if err := scanner.Err(); err != nil {
			ch <- core.StreamChunk{Error: fmt.Errorf("stream read error: %w", err)}
		}
	}()

	return ch, nil
}

// chatResponse mirrors the OpenAI chat completion response shape.
type chatResponse struct {
	ID      string        `json:"id"`
	Object  string        `json:"object"`
	Created int64         `json:"created"`
	Model   string        `json:"model"`
	Choices []core.Choice `json:"choices"`
	Usage   core.Usage    `json:"usage"`
}

type apiErrorResponse struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

type sseChunk struct {
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
