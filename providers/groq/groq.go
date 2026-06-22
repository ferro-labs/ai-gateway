// Package groq provides a client for the Groq API.
package groq

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/ferro-labs/ai-gateway/internal/discovery"
	providerhttp "github.com/ferro-labs/ai-gateway/internal/httpclient"
	"github.com/ferro-labs/ai-gateway/internal/openaicompat"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

const (
	// Name is the canonical identifier for the Groq provider.
	// Re-exported as providers.NameGroq in providers/names.go.
	Name           = "groq"
	defaultBaseURL = "https://api.groq.com/openai"
)

// Provider implements the core.Provider interface for Groq.
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
)

// New creates a new Groq provider.
func New(apiKey, baseURL string) (*Provider, error) {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("groq: invalid base URL %q: must be http or https with a host", baseURL)
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

// SupportedModels returns the static list of known Groq models.
func (p *Provider) SupportedModels() []string {
	return []string{
		"llama-3.3-70b-versatile",
		"llama-3.1-8b-instant",
		"mixtral-8x7b-32768",
		"gemma2-9b-it",
	}
}

// SupportsModel returns true if the model is supported by Groq.
func (p *Provider) SupportsModel(_ string) bool {
	return true
}

// Models returns structured model metadata.
func (p *Provider) Models() []core.ModelInfo {
	return core.ModelsFromList(p.name, p.SupportedModels())
}

// DiscoverModels fetches the live model list from the Groq /v1/models endpoint.
func (p *Provider) DiscoverModels(ctx context.Context) ([]core.ModelInfo, error) {
	return discovery.DiscoverOpenAICompatibleModels(ctx, p.httpClient, p.baseURL+"/v1/models", p.apiKey, p.name)
}

// Complete sends a chat completion request to Groq.
func (p *Provider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	return openaicompat.PostChat(ctx, openaicompat.ChatParams{
		HTTPClient: p.httpClient,
		URL:        p.baseURL + "/v1/chat/completions",
		Provider:   p.name,
		Label:      "groq",
		Headers:    map[string]string{"Authorization": "Bearer " + p.apiKey, "Content-Type": "application/json"},
	}, req)
}

// CompleteStream sends a streaming chat completion request to Groq.
func (p *Provider) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
	return openaicompat.PostStream(ctx, openaicompat.ChatParams{
		HTTPClient: p.httpClient,
		URL:        p.baseURL + "/v1/chat/completions",
		Provider:   p.name,
		Label:      "groq",
		Headers:    map[string]string{"Authorization": "Bearer " + p.apiKey, "Content-Type": "application/json"},
	}, req)
}
