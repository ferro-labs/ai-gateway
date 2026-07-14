// Package azurefoundry provides a client for the Azure AI Foundry API.
package azurefoundry

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	providerhttp "github.com/ferro-labs/ai-gateway/internal/httpclient"
	"github.com/ferro-labs/ai-gateway/providers/core"
	"github.com/ferro-labs/ai-gateway/providers/internal/openaicompat"
)

// Name is the canonical provider identifier.
const Name = "azure-foundry"

// Provider implements the Azure AI Foundry API client.
type Provider struct {
	name    string
	apiKey  string
	baseURL string
	// apiVersion is retained for back-compat (the APIVersion() accessor and the
	// AZURE_FOUNDRY_API_VERSION env knob); it is not sent on the GA /openai/v1
	// route this provider targets. See New().
	apiVersion string
	httpClient *http.Client
}

// Compile-time interface assertions.
var (
	_ core.Provider              = (*Provider)(nil)
	_ core.StreamProvider        = (*Provider)(nil)
	_ core.ProxiableProvider     = (*Provider)(nil)
	_ core.NonOpenAIWireProvider = (*Provider)(nil)
)

// New creates a new Azure AI Foundry provider.
func New(apiKey, baseURL, apiVersion string) (*Provider, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("baseURL is required for azure-foundry provider")
	}
	if err := core.ValidateBaseURL(Name, baseURL); err != nil {
		return nil, err
	}
	// apiVersion is retained for the APIVersion() accessor and backward
	// compatibility; the GA /openai/v1 route this provider targets does not take
	// an api-version query parameter.
	if apiVersion == "" {
		apiVersion = "2024-05-01-preview"
	}
	return &Provider{
		name:       Name,
		apiKey:     apiKey,
		baseURL:    baseURL,
		apiVersion: apiVersion,
		httpClient: providerhttp.ForProvider(Name),
	}, nil
}

// Name implements core.Provider.
func (p *Provider) Name() string { return p.name }

// BaseURL implements core.ProxiableProvider.
func (p *Provider) BaseURL() string { return p.baseURL }

// APIVersion returns the configured Azure API version. Retained for back-compat;
// it is not sent on the GA /openai/v1 route.
func (p *Provider) APIVersion() string { return p.apiVersion }

// NonOpenAIWire marks Azure AI Foundry as ineligible for transparent
// OpenAI-wire proxy pass-through: its upstream uses Azure AI Foundry
// deployment/routing paths and api-key auth, so an OpenAI-shaped request is not
// directly forwardable. It remains fully usable via its native translated
// endpoints. See core.NonOpenAIWireProvider.
func (*Provider) NonOpenAIWire() {}

// AuthHeaders implements core.ProxiableProvider.
func (p *Provider) AuthHeaders() map[string]string {
	return map[string]string{"api-key": p.apiKey}
}

// SupportedModels returns known Azure Foundry models.
func (p *Provider) SupportedModels() []string {
	return []string{
		"gpt-4o",
		"gpt-4.1",
		"phi-4",
	}
}

// SupportsModel returns true for any model — Azure Foundry validates model names.
func (p *Provider) SupportsModel(_ string) bool { return true }

// Models returns structured model metadata.
func (p *Provider) Models() []core.ModelInfo {
	return core.ModelsFromList(p.name, p.SupportedModels())
}

// endpoint targets the GA OpenAI v1-compatible route. The older Model Inference
// "/models" route (with an api-version query parameter) is retired by the
// vendor; the v1 route is OpenAI-shaped, takes no api-version, and still routes
// cross-provider Foundry models via the request "model" field.
func (p *Provider) endpoint() string {
	return p.baseURL + "/openai/v1/chat/completions"
}

// chatParams builds the shared OpenAI-compatible request parameters.
// "extra-parameters: drop" asks Foundry to ignore any non-schema field the
// gateway forwards rather than reject the request (the default is "error").
func (p *Provider) chatParams() openaicompat.ChatParams {
	return openaicompat.ChatParams{
		HTTPClient: p.httpClient,
		URL:        p.endpoint(),
		Provider:   p.name,
		Label:      "azure foundry",
		Headers: map[string]string{
			"api-key":          p.apiKey,
			"Content-Type":     "application/json",
			"extra-parameters": "drop",
		},
	}
}

// Complete sends a chat completion request to Azure AI Foundry.
func (p *Provider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	return openaicompat.PostChat(ctx, p.chatParams(), req)
}

// CompleteStream sends a streaming chat completion request to Azure AI Foundry.
func (p *Provider) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
	return openaicompat.PostStream(ctx, p.chatParams(), req)
}
