// Package openrouter provides a client for the OpenRouter API.
package openrouter

import (
	"context"
	"net/http"

	providerhttp "github.com/ferro-labs/ai-gateway/internal/httpclient"
	"github.com/ferro-labs/ai-gateway/providers/core"
	"github.com/ferro-labs/ai-gateway/providers/core/openaicompat"
)

const (
	// Name is the canonical identifier for the OpenRouter provider.
	// Re-exported as providers.NameOpenRouter in providers/names.go.
	Name           = "openrouter"
	defaultBaseURL = "https://openrouter.ai/api/v1"
)

// Provider implements the core.Provider interface for OpenRouter.
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
	_ core.DiscoveryProvider = (*Provider)(nil)
	_ core.EmbeddingProvider = (*Provider)(nil)
	_ core.AnyModelProvider  = (*Provider)(nil)
)

// New creates a new OpenRouter provider.
func New(apiKey, baseURL string) (*Provider, error) {
	baseURL, err := core.ResolveAPIRoot(Name, baseURL, defaultBaseURL)
	if err != nil {
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

// SupportsModel returns true for any OpenRouter model name.
func (p *Provider) SupportsModel(_ string) bool { return true }

// ServesAnyModel declares core.AnyModelProvider: OpenRouter is an aggregator of
// 400+ upstream models whose roster changes continuously, so a catalog snapshot
// is stale the day it ships and live discovery is opt-in. Without this, a real
// model this target serves was refused 404 by the routing index and the request
// never reached OpenRouter at all — which is the whole reason to configure an
// aggregator. It stays additive: a model the index already has an owner for is
// routed to that owner and never offered here.
func (p *Provider) ServesAnyModel() {}

// DiscoverModels fetches the live model list from the OpenRouter /models endpoint.
func (p *Provider) DiscoverModels(ctx context.Context) ([]core.ModelInfo, error) {
	return core.DiscoverOpenAICompatibleModels(ctx, p.httpClient, p.baseURL+"/models", p.apiKey, p.name)
}

// Complete sends a chat completion request to OpenRouter.
func (p *Provider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	return openaicompat.PostChat(ctx, p.chatParams(), req)
}

// chatParams builds the shared OpenAI-compatible chat endpoint configuration.
func (p *Provider) chatParams() openaicompat.ChatParams {
	return openaicompat.ChatParams{
		HTTPClient: p.httpClient,
		URL:        p.baseURL + "/chat/completions",
		Provider:   p.name,
		Label:      "openrouter",
		Headers:    map[string]string{"Authorization": "Bearer " + p.apiKey, "Content-Type": "application/json"},
	}
}

// CompleteStream sends a streaming chat completion request to OpenRouter.
func (p *Provider) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
	return openaicompat.PostStream(ctx, p.chatParams(), req)
}
