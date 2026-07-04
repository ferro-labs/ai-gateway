// Package xai provides a client for the xAI (Grok) API.
package xai

import (
	"context"
	"net/http"
	"strings"

	"github.com/ferro-labs/ai-gateway/internal/discovery"
	providerhttp "github.com/ferro-labs/ai-gateway/internal/httpclient"
	"github.com/ferro-labs/ai-gateway/providers/core"
	"github.com/ferro-labs/ai-gateway/providers/internal/openaicompat"
)

const (
	// Name is the canonical identifier for the xAI provider.
	// Re-exported as providers.NameXAI in providers/names.go.
	Name           = "xai"
	defaultBaseURL = "https://api.x.ai/v1"
)

// Provider implements the core.Provider interface for xAI.
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
	_ core.DiscoveryProvider = (*Provider)(nil)
	_ core.ProxiableProvider = (*Provider)(nil)
	_ core.ImageProvider     = (*Provider)(nil)
)

// New creates a new xAI provider.
func New(apiKey, baseURL string) (*Provider, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	if err := core.ValidateBaseURL(Name, baseURL); err != nil {
		return nil, err
	}
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
	return map[string]string{"Authorization": "Bearer " + p.apiKey}
}

// SupportedModels returns the static list of known xAI models.
func (p *Provider) SupportedModels() []string {
	// grok-2-latest is kept first because the model catalog resolves it as
	// xai/grok-2-latest (see models/catalog_backup.json). SupportsModel accepts
	// any grok*/xai* name by prefix, so the grok-3/grok-4 entries below are for
	// discovery/listing surfaces.
	return []string{
		"grok-2-latest",
		"grok-2-vision-latest",
		"grok-beta",
		"grok-3",
		"grok-3-mini",
		"grok-4",
		"grok-4-latest",
		"grok-4-fast-reasoning",
		"grok-4-fast-non-reasoning",
		"grok-code-fast-1",
		"grok-2-image",
		"grok-2-image-1212",
		"grok-2-image-latest",
	}
}

// SupportsModel returns true for Grok/xAI model names.
func (p *Provider) SupportsModel(model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(normalized, "grok") || strings.HasPrefix(normalized, "xai")
}

// Models returns structured model metadata.
func (p *Provider) Models() []core.ModelInfo {
	return core.ModelsFromList(p.name, p.SupportedModels())
}

// DiscoverModels fetches the live model list from the xAI /models endpoint.
func (p *Provider) DiscoverModels(ctx context.Context) ([]core.ModelInfo, error) {
	return discovery.DiscoverOpenAICompatibleModels(ctx, p.httpClient, p.baseURL+"/models", p.apiKey, p.name)
}

// chatParams builds the shared OpenAI-compatible chat endpoint configuration.
func (p *Provider) chatParams() openaicompat.ChatParams {
	return openaicompat.ChatParams{
		HTTPClient: p.httpClient,
		URL:        p.baseURL + "/chat/completions",
		Provider:   p.name,
		Label:      "xai",
		Headers:    map[string]string{"Authorization": "Bearer " + p.apiKey, "Content-Type": "application/json"},
	}
}

// Complete sends a chat completion request to xAI.
func (p *Provider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	return openaicompat.PostChat(ctx, p.chatParams(), req)
}

// CompleteStream sends a streaming chat completion request to xAI.
func (p *Provider) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
	return openaicompat.PostStream(ctx, p.chatParams(), req)
}
