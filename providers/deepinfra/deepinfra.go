// Package deepinfra provides a client for the DeepInfra OpenAI-compatible API.
package deepinfra

import (
	"context"
	"net/http"
	"strings"

	providerhttp "github.com/ferro-labs/ai-gateway/internal/httpclient"
	"github.com/ferro-labs/ai-gateway/providers/core"
	"github.com/ferro-labs/ai-gateway/providers/core/openaicompat"
)

const (
	// Name is the canonical identifier for the DeepInfra provider.
	// Re-exported as providers.NameDeepInfra in providers/names.go.
	Name           = "deepinfra"
	defaultBaseURL = "https://api.deepinfra.com/v1/openai"
)

// Provider implements the core.Provider interface for DeepInfra.
type Provider struct {
	name    string
	apiKey  string
	baseURL string
	// inferenceBaseURL is the root of DeepInfra's native inference API
	// (/v1/inference/{model}), the sibling of the OpenAI-compat root rerank is
	// served from. Resolved once here, like baseURL, so no request-time code
	// reinterprets the configured base.
	inferenceBaseURL string
	httpClient       *http.Client
}

var (
	_ core.Provider              = (*Provider)(nil)
	_ core.StreamProvider        = (*Provider)(nil)
	_ core.ProxiableProvider     = (*Provider)(nil)
	_ core.EmbeddingProvider     = (*Provider)(nil)
	_ core.ImageProvider         = (*Provider)(nil)
	_ core.RerankProvider        = (*Provider)(nil)
	_ core.DiscoveryProvider     = (*Provider)(nil)
	_ core.TranscriptionProvider = (*Provider)(nil)
	_ core.SpeechProvider        = (*Provider)(nil)
)

// New creates a new DeepInfra provider.
func New(apiKey, baseURL string) (*Provider, error) {
	baseURL, err := core.ResolveAPIRoot(Name, baseURL, defaultBaseURL)
	if err != nil {
		return nil, err
	}
	// The native inference root is the OpenAI-compat root's sibling: the vendor
	// mounts the compat surface at <root>/openai, so stripping that suffix lands
	// on /v1/inference. A base with no /openai suffix (a proxy at its own mount
	// point) keeps the convention every other surface follows — the operation
	// path is appended to the operator's root as written.
	return &Provider{
		name:             Name,
		apiKey:           apiKey,
		baseURL:          baseURL,
		inferenceBaseURL: strings.TrimSuffix(baseURL, "/openai") + "/inference",
		httpClient:       providerhttp.ForProvider(Name),
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

// SupportsModel returns true for any model name.
func (p *Provider) SupportsModel(_ string) bool { return true }

// DiscoverModels fetches the live model list from the DeepInfra /models endpoint.
func (p *Provider) DiscoverModels(ctx context.Context) ([]core.ModelInfo, error) {
	return core.DiscoverOpenAICompatibleModels(ctx, p.httpClient, p.baseURL+"/models", p.apiKey, p.name)
}

// Complete sends a chat completion request to DeepInfra.
func (p *Provider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	return openaicompat.PostChat(ctx, openaicompat.ChatParams{
		HTTPClient: p.httpClient,
		URL:        p.baseURL + "/chat/completions",
		Provider:   p.name,
		Label:      "deepinfra",
		Headers:    map[string]string{"Authorization": "Bearer " + p.apiKey, "Content-Type": "application/json"},
	}, req)
}

// CompleteStream sends a streaming chat completion request to DeepInfra.
func (p *Provider) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
	return openaicompat.PostStream(ctx, openaicompat.ChatParams{
		HTTPClient: p.httpClient,
		URL:        p.baseURL + "/chat/completions",
		Provider:   p.name,
		Label:      "deepinfra",
		Headers:    map[string]string{"Authorization": "Bearer " + p.apiKey, "Content-Type": "application/json"},
	}, req)
}
