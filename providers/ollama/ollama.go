// Package ollama provides a client for the Ollama local LLM server.
package ollama

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	providerhttp "github.com/ferro-labs/ai-gateway/internal/httpclient"
	"github.com/ferro-labs/ai-gateway/internal/openaicompat"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// Name is the canonical provider identifier.
const Name = "ollama"

const defaultBaseURL = "http://localhost:11434"

// Provider implements the Ollama API client.
type Provider struct {
	name       string
	baseURL    string
	httpClient *http.Client
	models     []string
}

// Compile-time interface assertions.
var (
	_ core.Provider          = (*Provider)(nil)
	_ core.StreamProvider    = (*Provider)(nil)
	_ core.ProxiableProvider = (*Provider)(nil)
	_ core.EmbeddingProvider = (*Provider)(nil)
	_ core.DiscoveryProvider = (*Provider)(nil)
)

// New creates a new Ollama provider.
func New(baseURL string, models []string) (*Provider, error) {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")

	if len(models) == 0 {
		models = []string{"llama3.2"}
	}

	return &Provider{
		name:       Name,
		baseURL:    baseURL,
		httpClient: providerhttp.ForProvider(Name),
		models:     models,
	}, nil
}

// Name implements core.Provider.
func (p *Provider) Name() string { return p.name }

// BaseURL implements core.ProxiableProvider.
func (p *Provider) BaseURL() string { return p.baseURL }

// AuthHeaders implements core.ProxiableProvider.
// Ollama is a local server with no API key requirement.
func (p *Provider) AuthHeaders() map[string]string { return nil }

// SupportedModels returns the configured models.
func (p *Provider) SupportedModels() []string { return p.models }

// SupportsModel returns true for any model — the upstream server validates model names.
func (p *Provider) SupportsModel(_ string) bool { return true }

// Models returns structured model metadata.
func (p *Provider) Models() []core.ModelInfo {
	return core.ModelsFromList(p.name, p.SupportedModels())
}

type ollamaResponse struct {
	ID      string        `json:"id"`
	Model   string        `json:"model"`
	Choices []core.Choice `json:"choices"`
	Usage   core.Usage    `json:"usage"`
}

type ollamaErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

type ollamaErrorResponse struct {
	Error ollamaErrorDetail `json:"error"`
}

// Complete sends a chat completion request and returns the full response.
func (p *Provider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	bodyReader, _, release, err := openaicompat.BuildBody(req, false)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	defer release()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/chat/completions", bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
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
		var errResp ollamaErrorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return nil, fmt.Errorf("ollama API error (%d): %s", httpResp.StatusCode, errResp.Error.Message)
		}
		return nil, fmt.Errorf("ollama API error (%d): %s", httpResp.StatusCode, string(respBody))
	}

	var ollamaResp ollamaResponse
	if err := json.Unmarshal(respBody, &ollamaResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &core.Response{
		ID:      ollamaResp.ID,
		Model:   ollamaResp.Model,
		Choices: ollamaResp.Choices,
		Usage:   ollamaResp.Usage,
	}, nil
}

// CompleteStream sends a streaming chat completion request to Ollama.
func (p *Provider) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
	return openaicompat.PostStream(ctx, openaicompat.ChatParams{
		HTTPClient: p.httpClient,
		URL:        p.baseURL + "/v1/chat/completions",
		Provider:   p.name,
		Label:      "ollama",
		Headers:    map[string]string{"Content-Type": "application/json"},
	}, req)
}

type tagsResponse struct {
	Models []struct {
		Name       string `json:"name"`
		Model      string `json:"model"`
		ModifiedAt string `json:"modified_at"`
	} `json:"models"`
}

// DiscoverModels fetches the live model list from the self-hosted Ollama
// server's /api/tags endpoint. Ollama is unauthenticated, so no Authorization
// header is sent.
//
// Unlike ollama_cloud, this deliberately does NOT cache the discovered names
// for use by SupportsModel: for self-hosted Ollama, SupportsModel always
// returns true because the server validates model names itself, so there is
// nothing to track. Do not "fix" this to mirror ollama_cloud.
func (p *Provider) DiscoverModels(ctx context.Context) ([]core.ModelInfo, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/api/tags", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
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
		var errResp ollamaErrorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return nil, fmt.Errorf("ollama API error (%d): %s", httpResp.StatusCode, errResp.Error.Message)
		}
		return nil, fmt.Errorf("ollama API error (%d): %s", httpResp.StatusCode, string(respBody))
	}

	var tags tagsResponse
	if err := json.Unmarshal(respBody, &tags); err != nil {
		return nil, fmt.Errorf("failed to unmarshal models response: %w", err)
	}

	seen := make(map[string]struct{}, len(tags.Models))
	models := make([]core.ModelInfo, 0, len(tags.Models))
	for _, m := range tags.Models {
		id := strings.TrimSpace(m.Name)
		if id == "" {
			id = strings.TrimSpace(m.Model)
		}
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		models = append(models, core.ModelInfo{
			ID:      id,
			Object:  "model",
			Created: parseCreatedAt(m.ModifiedAt),
			OwnedBy: p.name,
		})
	}

	return models, nil
}

func parseCreatedAt(value string) int64 {
	if value == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return 0
	}
	return t.Unix()
}
