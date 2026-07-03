// Package mistral provides a client for the Mistral AI API.
package mistral

import (
	"context"
	"net/http"
	"strings"

	"github.com/ferro-labs/ai-gateway/internal/discovery"
	providerhttp "github.com/ferro-labs/ai-gateway/internal/httpclient"
	"github.com/ferro-labs/ai-gateway/internal/openaicompat"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

const (
	// Name is the canonical identifier for the Mistral provider.
	// Re-exported as providers.NameMistral in providers/names.go.
	Name           = "mistral"
	defaultBaseURL = "https://api.mistral.ai"
)

// Provider implements the core.Provider interface for Mistral.
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
	_ core.DiscoveryProvider = (*Provider)(nil)
)

// New creates a new Mistral provider.
func New(apiKey, baseURL string) (*Provider, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = defaultBaseURL
	} else if err := core.ValidateBaseURL(Name, baseURL); err != nil {
		return nil, err
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

// SupportedModels returns the static list of known Mistral models.
func (p *Provider) SupportedModels() []string {
	return []string{
		"mistral-large-latest",
		"mistral-small-latest",
		"open-mistral-nemo",
		"codestral-latest",
		"mistral-embed",
		"codestral-embed",
		"codestral-embed-2505",
	}
}

// SupportsModel returns true if the model is supported by Mistral.
func (p *Provider) SupportsModel(model string) bool {
	for _, prefix := range []string{"mistral-", "codestral-", "open-mistral-", "pixtral-", "ministral-"} {
		if strings.HasPrefix(model, prefix) {
			return true
		}
	}
	return false
}

// Models returns structured model metadata.
func (p *Provider) Models() []core.ModelInfo {
	return core.ModelsFromList(p.name, p.SupportedModels())
}

// DiscoverModels fetches the live model list from the Mistral /v1/models endpoint.
func (p *Provider) DiscoverModels(ctx context.Context) ([]core.ModelInfo, error) {
	return discovery.DiscoverOpenAICompatibleModels(ctx, p.httpClient, p.baseURL+"/v1/models", p.apiKey, p.name)
}

// mistralChatBody reshapes the OpenAI-shaped chat body for Mistral's API, which
// ignores the standard "seed" field and instead honours "random_seed". The
// embedded core.Request forwards every other OpenAI field unchanged. The Seed
// field shadows the promoted core.Request.Seed at a shallower depth and is left
// nil (omitempty) so "seed" is never emitted on the wire; RandomSeed carries the
// value under Mistral's expected key.
//
// A json:"-" shadow does not work here: encoding/json drops "-" fields entirely,
// so the promoted core.Request.Seed would still be emitted. A same-named field
// that dominates by depth and is omitted via omitempty is what actually
// suppresses it (verified by TestMistralProvider_Complete_SeedRewrite).
type mistralChatBody struct {
	core.Request
	Seed       *int64 `json:"seed,omitempty"`        // shadows core.Request.Seed; always nil so "seed" is suppressed
	RandomSeed *int64 `json:"random_seed,omitempty"` // Mistral's seed field
}

// mistralChatTransform maps core.Request onto Mistral's chat body, renaming
// seed → random_seed.
func mistralChatTransform(req core.Request) any {
	return mistralChatBody{Request: req, RandomSeed: req.Seed}
}

// Complete sends a chat completion request to Mistral.
func (p *Provider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	return openaicompat.PostChat(ctx, openaicompat.ChatParams{
		HTTPClient:    p.httpClient,
		URL:           p.baseURL + "/v1/chat/completions",
		Provider:      p.name,
		Label:         "mistral",
		Headers:       map[string]string{"Authorization": "Bearer " + p.apiKey, "Content-Type": "application/json"},
		BodyTransform: mistralChatTransform,
	}, req)
}

// CompleteStream sends a streaming chat completion request to Mistral.
func (p *Provider) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
	return openaicompat.PostStream(ctx, openaicompat.ChatParams{
		HTTPClient:    p.httpClient,
		URL:           p.baseURL + "/v1/chat/completions",
		Provider:      p.name,
		Label:         "mistral",
		Headers:       map[string]string{"Authorization": "Bearer " + p.apiKey, "Content-Type": "application/json"},
		BodyTransform: mistralChatTransform,
	}, req)
}
