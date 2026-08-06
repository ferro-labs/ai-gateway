// Package groq provides a client for the Groq API.
package groq

import (
	"context"
	"net/http"

	providerhttp "github.com/ferro-labs/ai-gateway/internal/httpclient"
	"github.com/ferro-labs/ai-gateway/providers/core"
	"github.com/ferro-labs/ai-gateway/providers/core/openaicompat"
)

const (
	// Name is the canonical identifier for the Groq provider.
	// Re-exported as providers.NameGroq in providers/names.go.
	Name           = "groq"
	defaultBaseURL = "https://api.groq.com/openai/v1"
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
	_ core.Provider              = (*Provider)(nil)
	_ core.StreamProvider        = (*Provider)(nil)
	_ core.ProxiableProvider     = (*Provider)(nil)
	_ core.DiscoveryProvider     = (*Provider)(nil)
	_ core.TranscriptionProvider = (*Provider)(nil)
	_ core.SpeechProvider        = (*Provider)(nil)
	_ core.BatchProvider         = (*Provider)(nil)
)

// New creates a new Groq provider.
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

// Name implements core.Provider.
func (p *Provider) Name() string { return p.name }

// BaseURL implements core.ProxiableProvider.
func (p *Provider) BaseURL() string { return p.baseURL }

// AuthHeaders implements core.ProxiableProvider.
func (p *Provider) AuthHeaders() map[string]string {
	return map[string]string{"Authorization": "Bearer " + p.apiKey}
}

// SupportsModel returns true if the model is supported by Groq.
func (p *Provider) SupportsModel(_ string) bool {
	return true
}

// DiscoverModels fetches the live model list from the Groq /v1/models endpoint.
func (p *Provider) DiscoverModels(ctx context.Context) ([]core.ModelInfo, error) {
	return core.DiscoverOpenAICompatibleModels(ctx, p.httpClient, p.baseURL+"/models", p.apiKey, p.name)
}

// stripMessageNames returns req with every messages[].name cleared. Groq
// documents the field as one of a short list that "will result in a 400 error
// if supplied" (https://console.groq.com/docs/openai), so a request an OpenAI
// client sends unmodified — naming participants, or a library that sets it by
// default — failed outright here and nowhere else.
//
// This is provider-local on purpose. The capability matrix governs the
// top-level chat parameters, and one vendor's constraint on one message field
// does not justify a second declarative mechanism underneath it.
//
// The messages slice is copied before clearing: req is a value, but its slice
// header points at the caller's backing array, and the caller keeps using that
// request for retries, plugins and logging.
func stripMessageNames(req core.Request) core.Request {
	named := false
	for i := range req.Messages {
		if req.Messages[i].Name != "" {
			named = true
			break
		}
	}
	if !named {
		return req
	}
	msgs := make([]core.Message, len(req.Messages))
	copy(msgs, req.Messages)
	for i := range msgs {
		msgs[i].Name = ""
	}
	req.Messages = msgs
	return req
}

// Complete sends a chat completion request to Groq.
func (p *Provider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	// Forward only the modern max_completion_tokens (mirrors the OpenAI surface).
	req.PreferCompletionTokens()
	req = stripMessageNames(req)
	return openaicompat.PostChat(ctx, openaicompat.ChatParams{
		HTTPClient: p.httpClient,
		URL:        p.baseURL + "/chat/completions",
		Provider:   p.name,
		Label:      "groq",
		Headers:    map[string]string{"Authorization": "Bearer " + p.apiKey, "Content-Type": "application/json"},
	}, req)
}

// CompleteStream sends a streaming chat completion request to Groq.
func (p *Provider) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
	// Forward only the modern max_completion_tokens (mirrors the OpenAI surface).
	req.PreferCompletionTokens()
	req = stripMessageNames(req)
	return openaicompat.PostStream(ctx, openaicompat.ChatParams{
		HTTPClient: p.httpClient,
		URL:        p.baseURL + "/chat/completions",
		Provider:   p.name,
		Label:      "groq",
		Headers:    map[string]string{"Authorization": "Bearer " + p.apiKey, "Content-Type": "application/json"},
	}, req)
}
