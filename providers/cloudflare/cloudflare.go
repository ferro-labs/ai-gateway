// Package cloudflare provides a client for Cloudflare Workers AI.
package cloudflare

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	providerhttp "github.com/ferro-labs/ai-gateway/internal/httpclient"
	"github.com/ferro-labs/ai-gateway/providers/core"
	"github.com/ferro-labs/ai-gateway/providers/internal/openaicompat"
)

const (
	// Name is the canonical identifier for the Cloudflare Workers AI provider.
	// Re-exported as providers.NameCloudflare in providers/names.go.
	Name           = "cloudflare"
	defaultBaseURL = "https://api.cloudflare.com/client/v4/accounts/%s/ai/v1"
)

// Provider implements the core.Provider interface for Cloudflare Workers AI.
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
	_ core.EmbeddingProvider = (*Provider)(nil)
	_ core.ProxiableProvider = (*Provider)(nil)
)

// New creates a new Cloudflare Workers AI provider.
func New(apiKey, accountID, baseURL string) (*Provider, error) {
	if strings.TrimSpace(accountID) == "" && strings.TrimSpace(baseURL) == "" {
		return nil, fmt.Errorf("cloudflare: accountID or baseURL is required")
	}
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = fmt.Sprintf(defaultBaseURL, strings.TrimSpace(accountID))
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

// SupportedModels returns a static list of known Cloudflare Workers AI models.
func (p *Provider) SupportedModels() []string {
	return []string{
		"@cf/meta/llama-3.1-8b-instruct",
		"@cf/openai/gpt-oss-120b",
		"@cf/baai/bge-large-en-v1.5",
		"@cf/meta/llama-3.2-3b-instruct",
		"@cf/meta/llama-4-scout-17b-16e-instruct",
	}
}

// SupportsModel returns true for any Cloudflare model identifier.
func (p *Provider) SupportsModel(_ string) bool { return true }

// Models returns structured model metadata.
func (p *Provider) Models() []core.ModelInfo {
	return core.ModelsFromList(p.name, p.SupportedModels())
}

// headers returns the auth + content-type headers for direct API calls.
func (p *Provider) headers() map[string]string {
	h := p.AuthHeaders()
	h["Content-Type"] = "application/json"
	return h
}

// chatParams builds the shared OpenAI-compatible chat endpoint configuration.
func (p *Provider) chatParams() openaicompat.ChatParams {
	return openaicompat.ChatParams{
		HTTPClient: p.httpClient,
		URL:        p.baseURL + "/chat/completions",
		Provider:   p.name,
		Label:      "cloudflare",
		Headers:    p.headers(),
	}
}

// Complete sends a chat completion request to Cloudflare Workers AI.
func (p *Provider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	return openaicompat.PostChat(ctx, p.chatParams(), req)
}

// CompleteStream sends a streaming chat completion request to Cloudflare Workers AI.
func (p *Provider) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
	return openaicompat.PostStream(ctx, p.chatParams(), req)
}

// Embed sends an OpenAI-compatible embedding request to Cloudflare Workers AI.
func (p *Provider) Embed(ctx context.Context, req core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return openaicompat.PostEmbeddings(ctx, openaicompat.EmbeddingParams{
		HTTPClient: p.httpClient,
		URL:        p.baseURL + "/embeddings",
		Headers:    p.headers(),
		Label:      "cloudflare",
	}, req)
}
