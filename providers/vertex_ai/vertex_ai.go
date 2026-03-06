// Package vertexai provides a client for Google Vertex AI.
package vertexai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// Name is the canonical provider identifier.
const Name = "vertex-ai"

// Options configures Vertex AI provider initialization.
type Options struct {
	ProjectID          string
	Region             string
	APIKey             string
	ServiceAccountJSON string
}

// Provider implements the Vertex AI API client.
type Provider struct {
	name        string
	apiKey      string
	baseURL     string
	httpClient  *http.Client
	tokenSource oauth2.TokenSource
}

// Compile-time interface assertions.
var (
	_ core.Provider          = (*Provider)(nil)
	_ core.StreamProvider    = (*Provider)(nil)
	_ core.ProxiableProvider = (*Provider)(nil)
)

// New creates a new Vertex AI provider.
// Supports API key mode and service-account JSON mode.
func New(opts Options) (*Provider, error) {
	projectID := strings.TrimSpace(opts.ProjectID)
	if projectID == "" {
		return nil, fmt.Errorf("project_id is required for vertex-ai provider")
	}
	region := strings.TrimSpace(opts.Region)
	if region == "" {
		return nil, fmt.Errorf("region is required for vertex-ai provider")
	}

	apiKey := strings.TrimSpace(opts.APIKey)
	serviceAccountJSON := strings.TrimSpace(opts.ServiceAccountJSON)
	if apiKey == "" && serviceAccountJSON == "" {
		return nil, fmt.Errorf("either api key or service account JSON is required for vertex-ai provider")
	}

	var tokenSource oauth2.TokenSource
	if serviceAccountJSON != "" {
		cfg, err := google.JWTConfigFromJSON([]byte(serviceAccountJSON), "https://www.googleapis.com/auth/cloud-platform")
		if err != nil {
			return nil, fmt.Errorf("invalid Vertex AI service account JSON: %w", err)
		}
		tokenSource = cfg.TokenSource(context.Background())
	}

	baseURL := fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/endpoints/openapi", region, projectID, region)
	return &Provider{
		name:        Name,
		apiKey:      apiKey,
		baseURL:     baseURL,
		httpClient:  &http.Client{},
		tokenSource: tokenSource,
	}, nil
}

// Name implements core.Provider.
func (p *Provider) Name() string { return p.name }

// BaseURL implements core.ProxiableProvider.
func (p *Provider) BaseURL() string { return p.baseURL }

// SetBaseURL overrides the base URL (used in tests to point to a mock server).
func (p *Provider) SetBaseURL(url string) { p.baseURL = url }

// AuthHeaders implements core.ProxiableProvider.
func (p *Provider) AuthHeaders() map[string]string {
	if p.apiKey != "" {
		return map[string]string{"x-goog-api-key": p.apiKey}
	}
	if p.tokenSource == nil {
		return map[string]string{}
	}
	tok, err := p.tokenSource.Token()
	if err != nil {
		return map[string]string{}
	}
	return map[string]string{"Authorization": "Bearer " + tok.AccessToken}
}

// SupportedModels returns known Vertex AI model examples.
func (p *Provider) SupportedModels() []string {
	return []string{
		"gemini-2.5-pro",
		"gemini-2.5-flash",
		"gemini-2.0-flash",
	}
}

// SupportsModel returns true for any model — Vertex AI validates model names.
func (p *Provider) SupportsModel(_ string) bool { return true }

// Models returns structured model metadata.
func (p *Provider) Models() []core.ModelInfo {
	return core.ModelsFromList(p.name, p.SupportedModels())
}

type vertexAIRequest struct {
	Model       string         `json:"model"`
	Messages    []core.Message `json:"messages"`
	Temperature *float64       `json:"temperature,omitempty"`
	MaxTokens   *int           `json:"max_tokens,omitempty"`
	Stream      bool           `json:"stream,omitempty"`
}

type vertexAIResponse struct {
	ID      string        `json:"id"`
	Model   string        `json:"model"`
	Choices []core.Choice `json:"choices"`
	Usage   core.Usage    `json:"usage"`
}

type vertexAIError struct {
	Error struct {
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

func (p *Provider) endpoint() string {
	return p.baseURL + "/chat/completions"
}

func (p *Provider) authorizeRequest(req *http.Request) error {
	if p.apiKey != "" {
		req.Header.Set("x-goog-api-key", p.apiKey)
		return nil
	}
	if p.tokenSource == nil {
		return fmt.Errorf("vertex-ai authorization is not configured")
	}
	tok, err := p.tokenSource.Token()
	if err != nil {
		return fmt.Errorf("vertex-ai token fetch failed: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	return nil
}

// Complete sends a chat completion request to Vertex AI.
func (p *Provider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	vertexReq := vertexAIRequest{
		Model:       req.Model,
		Messages:    req.Messages,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	}

	body, err := json.Marshal(vertexReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if err := p.authorizeRequest(httpReq); err != nil {
		return nil, err
	}

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
		var errResp vertexAIError
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return nil, fmt.Errorf("vertex ai API error (%d): %s", httpResp.StatusCode, errResp.Error.Message)
		}
		return nil, fmt.Errorf("vertex ai API error (%d): %s", httpResp.StatusCode, string(respBody))
	}

	var vertexResp vertexAIResponse
	if err := json.Unmarshal(respBody, &vertexResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &core.Response{
		ID:       vertexResp.ID,
		Model:    vertexResp.Model,
		Provider: p.name,
		Choices:  vertexResp.Choices,
		Usage:    vertexResp.Usage,
	}, nil
}

type vertexAIStreamResponse struct {
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

// CompleteStream sends a streaming chat completion request to Vertex AI.
func (p *Provider) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
	vertexReq := vertexAIRequest{
		Model:       req.Model,
		Messages:    req.Messages,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		Stream:      true,
	}

	body, err := json.Marshal(vertexReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if err := p.authorizeRequest(httpReq); err != nil {
		return nil, err
	}

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		defer func() { _ = httpResp.Body.Close() }()
		respBody, _ := io.ReadAll(httpResp.Body)
		var errResp vertexAIError
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return nil, fmt.Errorf("vertex ai API error (%d): %s", httpResp.StatusCode, errResp.Error.Message)
		}
		return nil, fmt.Errorf("vertex ai API error (%d): %s", httpResp.StatusCode, string(respBody))
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

			var chunk vertexAIStreamResponse
			if json.Unmarshal([]byte(data), &chunk) != nil {
				continue
			}

			sc := core.StreamChunk{ID: chunk.ID, Model: chunk.Model}
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
