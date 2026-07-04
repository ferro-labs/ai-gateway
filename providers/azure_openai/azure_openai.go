// Package azureopenai provides a client for the Azure OpenAI API.
package azureopenai

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	providerhttp "github.com/ferro-labs/ai-gateway/internal/httpclient"
	"github.com/ferro-labs/ai-gateway/providers/core"
	"github.com/ferro-labs/ai-gateway/providers/internal/openaicompat"
)

// Name is the canonical provider identifier.
const Name = "azure-openai"

const defaultAPIVersion = "2024-10-21"

// Provider implements the Azure OpenAI API client.
type Provider struct {
	name           string
	apiKey         string
	baseURL        string
	deploymentName string
	apiVersion     string
	httpClient     *http.Client
}

// Compile-time interface assertions.
var (
	_ core.Provider              = (*Provider)(nil)
	_ core.StreamProvider        = (*Provider)(nil)
	_ core.ProxiableProvider     = (*Provider)(nil)
	_ core.NonOpenAIWireProvider = (*Provider)(nil)
	_ core.EmbeddingProvider     = (*Provider)(nil)
	_ core.ImageProvider         = (*Provider)(nil)
)

// New creates a new Azure OpenAI provider.
func New(apiKey, baseURL, deploymentName, apiVersion string) (*Provider, error) {
	baseURL = strings.TrimSpace(baseURL)
	if err := core.ValidateBaseURL(Name, baseURL); err != nil {
		return nil, err
	}
	baseURL = strings.TrimRight(baseURL, "/")
	if apiVersion == "" {
		apiVersion = defaultAPIVersion
	}
	return &Provider{
		name:           Name,
		apiKey:         apiKey,
		baseURL:        baseURL,
		deploymentName: deploymentName,
		apiVersion:     apiVersion,
		httpClient:     providerhttp.ForProvider(Name),
	}, nil
}

// Name implements core.Provider.
func (p *Provider) Name() string { return p.name }

// BaseURL implements core.ProxiableProvider.
func (p *Provider) BaseURL() string { return p.baseURL }

// APIVersion returns the configured Azure API version.
func (p *Provider) APIVersion() string { return p.apiVersion }

// NonOpenAIWire marks Azure OpenAI as ineligible for transparent OpenAI-wire
// proxy pass-through: its upstream uses Azure deployment paths and an
// api-version query parameter, so an OpenAI-shaped request is not directly
// forwardable. It remains fully usable via its native translated endpoints. See
// core.NonOpenAIWireProvider.
func (*Provider) NonOpenAIWire() {}

// AuthHeaders implements core.ProxiableProvider.
func (p *Provider) AuthHeaders() map[string]string {
	return map[string]string{"api-key": p.apiKey}
}

// SupportedModels returns the deployment name as the only supported model.
func (p *Provider) SupportedModels() []string {
	return []string{p.deploymentName}
}

// SupportsModel returns true for any model — the upstream provider validates model names.
func (p *Provider) SupportsModel(_ string) bool { return true }

// Models returns structured model metadata.
func (p *Provider) Models() []core.ModelInfo {
	return []core.ModelInfo{
		{
			ID:      p.deploymentName,
			Object:  "model",
			OwnedBy: p.name,
		},
	}
}

func (p *Provider) endpoint() string {
	return fmt.Sprintf("%s/openai/deployments/%s/chat/completions?api-version=%s",
		p.baseURL, p.deploymentName, p.apiVersion)
}

// opEndpoint builds an Azure OpenAI URL for an arbitrary deployment+operation,
// e.g. op "embeddings" or "images/generations".
func (p *Provider) opEndpoint(deployment, op string) string {
	return fmt.Sprintf("%s/openai/deployments/%s/%s?api-version=%s",
		p.baseURL, url.PathEscape(deployment), op, p.apiVersion)
}

// deploymentFor selects the deployment to target for a request. Azure routes
// by deployment name in the URL (not by a body "model" field), so callers may
// override the configured deployment per request by setting req.Model.
//
// NOTE: Complete/CompleteStream intentionally keep using p.deploymentName
// (via endpoint()) rather than deploymentFor — the chat path is pinned to the
// single configured chat deployment, whereas Embed/GenerateImage allow the
// caller to target a different embedding/image deployment by model. This
// asymmetry is deliberate.
func (p *Provider) deploymentFor(model string) string {
	if model != "" {
		return model
	}
	return p.deploymentName
}

// chatParams builds the shared OpenAI-compatible request parameters for the
// configured chat deployment.
func (p *Provider) chatParams() openaicompat.ChatParams {
	return openaicompat.ChatParams{
		HTTPClient: p.httpClient,
		URL:        p.endpoint(),
		Headers:    map[string]string{"api-key": p.apiKey, "Content-Type": "application/json"},
		Provider:   p.name,
		Label:      "azure openai",
	}
}

// Complete sends a chat completion request to Azure OpenAI.
func (p *Provider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	// Azure o-series reasoning deployments reject max_tokens; keep only the
	// modern field (the gateway seam leaves both populated).
	req.PreferCompletionTokens()
	return openaicompat.PostChat(ctx, p.chatParams(), req)
}

// CompleteStream sends a streaming chat completion request to Azure OpenAI.
func (p *Provider) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
	req.PreferCompletionTokens()
	return openaicompat.PostStream(ctx, p.chatParams(), req)
}
