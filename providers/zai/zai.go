// Package zai provides a client for the Z.ai (Zhipu AI) API.
package zai

import (
	"context"

	"github.com/ferro-labs/ai-gateway/internal/compat"
	providerhttp "github.com/ferro-labs/ai-gateway/internal/httpclient"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

const (
	// Name is the canonical identifier for the Z.ai provider.
	// Re-exported as providers.NameZAI in providers/names.go.
	Name           = "zai"
	defaultBaseURL = "https://api.z.ai/api/paas/v4"
)

// Provider implements the core.Provider interface for Z.ai.
type Provider struct {
	*compat.Client
}

// Compile-time interface assertions.
var (
	_ core.Provider          = (*Provider)(nil)
	_ core.StreamProvider    = (*Provider)(nil)
	_ core.ProxiableProvider = (*Provider)(nil)
	_ core.DiscoveryProvider = (*Provider)(nil)
)

// New creates a new Z.ai provider.
func New(apiKey, baseURL string) (*Provider, error) {
	return &Provider{
		Client: compat.New(Name, apiKey, baseURL, defaultBaseURL, providerhttp.ForProvider(Name)),
	}, nil
}

// SupportedModels returns a static list of known Z.ai models.
func (p *Provider) SupportedModels() []string {
	return []string{
		"glm-5.1",
		"glm-5-turbo",
		"glm-5",
		"glm-4.7",
		"glm-4.6",
		"glm-4.5",
		"glm-4.5-air",
	}
}

// SupportsModel returns true for any Z.ai model name.
func (p *Provider) SupportsModel(_ string) bool { return true }

// Models returns structured model metadata.
func (p *Provider) Models() []core.ModelInfo {
	return core.ModelsFromList(p.Name(), p.SupportedModels())
}

// DiscoverModels fetches the live model list from the Z.ai /models endpoint.
func (p *Provider) DiscoverModels(ctx context.Context) ([]core.ModelInfo, error) {
	return p.Client.DiscoverModels(ctx)
}
