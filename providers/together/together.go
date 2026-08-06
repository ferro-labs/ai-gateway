// Package together provides a client for the Together AI API.
package together

import (
	"context"
	"net/http"

	providerhttp "github.com/ferro-labs/ai-gateway/internal/httpclient"
	"github.com/ferro-labs/ai-gateway/providers/core"
	"github.com/ferro-labs/ai-gateway/providers/core/openaicompat"
)

const (
	// Name is the canonical identifier for the Together AI provider.
	// Re-exported as providers.NameTogether in providers/names.go.
	Name = "together"
	// defaultBaseURL is Together AI's current documented API domain. Users
	// pinned to the legacy api.together.xyz host can override it via New's
	// baseURL argument.
	defaultBaseURL = "https://api.together.ai/v1"
)

// Provider implements the core.Provider interface for Together AI.
type Provider struct {
	name       string
	apiKey     string
	baseURL    string
	httpClient *http.Client
}

// Compile-time interface assertions.
var (
	_ core.Provider              = (*Provider)(nil)
	_ core.StreamProvider        = (*Provider)(nil)
	_ core.EmbeddingProvider     = (*Provider)(nil)
	_ core.ImageProvider         = (*Provider)(nil)
	_ core.RerankProvider        = (*Provider)(nil)
	_ core.ProxiableProvider     = (*Provider)(nil)
	_ core.DiscoveryProvider     = (*Provider)(nil)
	_ core.TranscriptionProvider = (*Provider)(nil)
	_ core.SpeechProvider        = (*Provider)(nil)
)

// New creates a new Together AI provider.
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

// headers returns the auth and content-type headers for Together AI requests.
func (p *Provider) headers() map[string]string {
	return map[string]string{
		"Authorization": "Bearer " + p.apiKey,
		"Content-Type":  "application/json",
	}
}

// Name implements core.Provider.
func (p *Provider) Name() string { return p.name }

// BaseURL implements core.ProxiableProvider.
func (p *Provider) BaseURL() string { return p.baseURL }

// AuthHeaders implements core.ProxiableProvider.
func (p *Provider) AuthHeaders() map[string]string {
	return map[string]string{"Authorization": "Bearer " + p.apiKey}
}

// SupportsModel returns true if the model is supported by Together AI.
func (p *Provider) SupportsModel(_ string) bool {
	return true
}

// DiscoverModels fetches the live model list from the Together AI /v1/models
// endpoint. Together returns a bare JSON array whose items omit owned_by, so the
// shared helper's dual-shape parser applies and owned_by falls back to "together".
func (p *Provider) DiscoverModels(ctx context.Context) ([]core.ModelInfo, error) {
	return core.DiscoverOpenAICompatibleModels(ctx, p.httpClient, p.baseURL+"/models", p.apiKey, p.name)
}

// togetherChatBody reshapes the OpenAI-shaped chat body for Together AI, which
// "consolidates [OpenAI's two-parameter convention] into a single numeric
// parameter" (https://docs.together.ai/docs/inference/chat/logprobs): logprobs
// is the COUNT of top tokens to return, and top_logprobs names a response field
// rather than a request one. Sending OpenAI's boolean logprobs and an undefined
// top_logprobs asked for log probabilities in a shape Together does not define,
// so a caller requesting them got no reliable answer.
//
// Both fields shadow their promoted core.Request counterparts at a shallower
// depth, which is what suppresses the originals — a json:"-" shadow would not,
// since encoding/json drops "-" fields and re-promotes the embedded ones.
// TopLogProbs stays nil so the key never reaches the wire.
type togetherChatBody struct {
	core.Request
	LogProbs    *int `json:"logprobs,omitempty"`     // shadows core.Request.LogProbs (bool): Together's count
	TopLogProbs *int `json:"top_logprobs,omitempty"` // shadows core.Request.TopLogProbs; always nil
}

// togetherChatTransform maps core.Request onto Together's chat body, folding
// logprobs+top_logprobs into Together's single logprobs count.
//
// The count comes from top_logprobs, which is what the OpenAI request means by
// it. A caller who asked for logprobs without naming a count gets 1: the field
// is required to carry a number here, and 1 is the smallest request that still
// returns what was asked for. logprobs:false forwards nothing at all, so an
// unrequested count is never invented.
func togetherChatTransform(req core.Request) any {
	body := togetherChatBody{Request: req}
	if req.LogProbs {
		count := 1
		if req.TopLogProbs != nil {
			count = *req.TopLogProbs
		}
		body.LogProbs = &count
	}
	return body
}

// chatParams builds the shared OpenAI-compatible chat endpoint configuration.
func (p *Provider) chatParams() openaicompat.ChatParams {
	return openaicompat.ChatParams{
		HTTPClient:    p.httpClient,
		URL:           p.baseURL + "/chat/completions",
		Provider:      p.name,
		Label:         "together",
		Headers:       p.headers(),
		BodyTransform: togetherChatTransform,
	}
}

// Complete sends a chat completion request to Together AI.
func (p *Provider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	return openaicompat.PostChat(ctx, p.chatParams(), req)
}

// CompleteStream sends a streaming chat completion request to Together AI.
func (p *Provider) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
	return openaicompat.PostStream(ctx, p.chatParams(), req)
}
