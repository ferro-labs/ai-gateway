// Package cloudflare provides a client for Cloudflare Workers AI.
package cloudflare

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	providerhttp "github.com/ferro-labs/ai-gateway/internal/httpclient"
	"github.com/ferro-labs/ai-gateway/providers/core"
	"github.com/ferro-labs/ai-gateway/providers/core/openaicompat"
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
	_ core.AnyModelProvider  = (*Provider)(nil)
)

// New creates a new Cloudflare Workers AI provider.
func New(apiKey, accountID, baseURL string) (*Provider, error) {
	if strings.TrimSpace(accountID) == "" && strings.TrimSpace(baseURL) == "" {
		return nil, fmt.Errorf("cloudflare: accountID or baseURL is required")
	}
	// When baseURL is set, defaultRoot is consulted only for its trailing
	// version segment, so an empty account slot in it is harmless.
	defaultRoot := fmt.Sprintf(defaultBaseURL, strings.TrimSpace(accountID))
	baseURL, err := core.ResolveAPIRoot(Name, baseURL, defaultRoot)
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

// SupportsModel returns true for any Cloudflare model identifier.
// Advisory only; see core.Provider.
func (p *Provider) SupportsModel(_ string) bool { return true }

// ServesAnyModel declares core.AnyModelProvider. Workers AI model ids (@cf/…)
// turn over faster than a catalog release and the provider enumerates none of
// them, so the index would otherwise serve only the handful the catalog carries.
// Narrowing this to the @cf/ namespace is the upgrade path if the declaration
// ever proves too wide.
func (p *Provider) ServesAnyModel() {}

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
	if err := core.ValidateEmbeddingEncodingFormat(req.EncodingFormat); err != nil {
		return nil, err
	}
	return openaicompat.PostEmbeddings(ctx, openaicompat.EmbeddingParams{
		HTTPClient: p.httpClient,
		URL:        p.baseURL + "/embeddings",
		Headers:    p.headers(),
		Label:      "cloudflare",
	}, req)
}
