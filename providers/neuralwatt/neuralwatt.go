// Package neuralwatt provides a client for the NeuralWatt energy-efficient inference API.
package neuralwatt

import (
	"context"
	"net/http"
	"strings"

	discov "github.com/ferro-labs/ai-gateway/internal/discovery"
	providerhttp "github.com/ferro-labs/ai-gateway/internal/httpclient"
	"github.com/ferro-labs/ai-gateway/internal/openaicompat"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

const (
	// Name is the canonical identifier for the NeuralWatt provider.
	// Re-exported as providers.NameNeuralWatt in providers/names.go.
	Name           = "neuralwatt"
	defaultBaseURL = "https://api.neuralwatt.com/v1"
)

// Provider implements the core.Provider interface for NeuralWatt.
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

// New creates a new NeuralWatt provider.
func New(apiKey, baseURL string) (*Provider, error) {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
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

// SupportedModels returns a static list of known NeuralWatt models.
func (p *Provider) SupportedModels() []string {
	return []string{
		"meta-llama/Llama-3.3-70B-Instruct",
		"zai-org/GLM-5.1-FP8",
		"meta-llama/Llama-3.1-8B-Instruct",
	}
}

// SupportsModel returns true if the model is in the known list or when live
// discovery is used (all models are accepted via passthrough).
func (p *Provider) SupportsModel(model string) bool {
	for _, m := range p.SupportedModels() {
		if m == model {
			return true
		}
	}
	// Accept any model name — live discovery may surface models not in the
	// static list above.
	return true
}

// Models returns structured model metadata.
func (p *Provider) Models() []core.ModelInfo {
	return core.ModelsFromList(p.name, p.SupportedModels())
}

// DiscoverModels fetches the live model list from the NeuralWatt /models endpoint.
// The /models endpoint is publicly accessible without authentication.
func (p *Provider) DiscoverModels(ctx context.Context) ([]core.ModelInfo, error) {
	return discov.DiscoverOpenAICompatibleModels(ctx, p.httpClient, p.baseURL+"/models", p.apiKey, p.name)
}

// Complete sends a chat completion request to NeuralWatt.
func (p *Provider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	return openaicompat.PostChat(ctx, openaicompat.ChatParams{
		HTTPClient: p.httpClient,
		URL:        p.baseURL + "/chat/completions",
		Provider:   p.name,
		Label:      "neuralwatt",
		Headers:    map[string]string{"Authorization": "Bearer " + p.apiKey, "Content-Type": "application/json"},
	}, req)
}

// CompleteStream sends a streaming chat completion request to NeuralWatt.
func (p *Provider) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
	return openaicompat.PostStream(ctx, openaicompat.ChatParams{
		HTTPClient: p.httpClient,
		URL:        p.baseURL + "/chat/completions",
		Provider:   p.name,
		Label:      "neuralwatt",
		Headers:    map[string]string{"Authorization": "Bearer " + p.apiKey, "Content-Type": "application/json"},
	}, req)
}
