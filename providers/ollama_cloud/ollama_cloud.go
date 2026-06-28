// Package ollamacloud provides a client for the Ollama Cloud API (api.ollama.com).
// Ollama Cloud exposes an OpenAI-compatible API at /v1/chat/completions and /v1/models,
// distinct from the local Ollama server's native /api/chat format.
package ollamacloud

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"

	discov "github.com/ferro-labs/ai-gateway/internal/discovery"
	providerhttp "github.com/ferro-labs/ai-gateway/internal/httpclient"
	"github.com/ferro-labs/ai-gateway/internal/openaicompat"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

const (
	// Name is the canonical provider identifier.
	Name           = "ollama-cloud"
	defaultBaseURL = "https://api.ollama.com"
)

var defaultModels = []string{
	"gpt-oss:120b",
	"gpt-oss:20b",
	"qwen3-coder:480b",
	"deepseek-v3.1:671b",
}

// Provider implements the Ollama Cloud API client.
type Provider struct {
	name       string
	apiKey     string
	baseURL    string
	httpClient *http.Client

	mu         sync.RWMutex
	models     []string
	discovered []string
}

// Compile-time interface assertions.
var (
	_ core.Provider          = (*Provider)(nil)
	_ core.StreamProvider    = (*Provider)(nil)
	_ core.DiscoveryProvider = (*Provider)(nil)
)

// New creates a new Ollama Cloud provider.
func New(apiKey, baseURL string, models []string) (*Provider, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("ollama-cloud: api key is required")
	}

	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("ollama-cloud: invalid base URL %q: must be http or https with a host", baseURL)
	}

	models = normalizeModels(models)
	if len(models) == 0 {
		models = append([]string(nil), defaultModels...)
	}

	return &Provider{
		name:       Name,
		apiKey:     apiKey,
		baseURL:    baseURL,
		httpClient: providerhttp.ForProvider(Name),
		models:     models,
	}, nil
}

// Name implements core.Provider.
func (p *Provider) Name() string { return p.name }

// SupportedModels returns the configured and discovered models.
func (p *Provider) SupportedModels() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return combineModels(p.models, p.discovered)
}

// SupportsModel returns true if model is configured or has been discovered.
func (p *Provider) SupportsModel(model string) bool {
	model = strings.TrimSpace(model)
	model = strings.TrimPrefix(model, Name+"/")
	if model == "" {
		return false
	}

	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, supported := range combineModels(p.models, p.discovered) {
		if model == supported {
			return true
		}
	}
	return false
}

// Models returns structured model metadata.
func (p *Provider) Models() []core.ModelInfo {
	return core.ModelsFromList(p.name, p.SupportedModels())
}

func (p *Provider) authHeaders() map[string]string {
	return map[string]string{
		"Authorization": "Bearer " + p.apiKey,
		"Content-Type":  "application/json",
	}
}

// Complete sends a non-streaming chat request to Ollama Cloud.
func (p *Provider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	return openaicompat.PostChat(ctx, openaicompat.ChatParams{
		HTTPClient: p.httpClient,
		URL:        p.baseURL + "/v1/chat/completions",
		Provider:   p.name,
		Label:      "ollama-cloud",
		Headers:    p.authHeaders(),
	}, req)
}

// CompleteStream sends a streaming chat request to Ollama Cloud.
func (p *Provider) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
	return openaicompat.PostStream(ctx, openaicompat.ChatParams{
		HTTPClient: p.httpClient,
		URL:        p.baseURL + "/v1/chat/completions",
		Provider:   p.name,
		Label:      "ollama-cloud",
		Headers:    p.authHeaders(),
	}, req)
}

// DiscoverModels fetches the live Ollama Cloud model list via the OpenAI-compat /v1/models endpoint.
func (p *Provider) DiscoverModels(ctx context.Context) ([]core.ModelInfo, error) {
	models, err := discov.DiscoverOpenAICompatibleModels(ctx, p.httpClient, p.baseURL+"/v1/models", p.apiKey, p.name)
	if err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(models))
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	p.mu.Lock()
	p.discovered = ids
	p.mu.Unlock()

	return models, nil
}

func normalizeModels(models []string) []string {
	out := make([]string, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		out = append(out, model)
	}
	return out
}

func combineModels(primary, secondary []string) []string {
	out := make([]string, 0, len(primary)+len(secondary))
	seen := make(map[string]struct{}, len(primary)+len(secondary))
	for _, model := range primary {
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		out = append(out, model)
	}
	for _, model := range secondary {
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		out = append(out, model)
	}
	return out
}
