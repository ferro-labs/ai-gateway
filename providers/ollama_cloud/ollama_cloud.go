// Package ollamacloud provides a client for the Ollama Cloud API.
package ollamacloud

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	discov "github.com/ferro-labs/ai-gateway/internal/discovery"
	providerhttp "github.com/ferro-labs/ai-gateway/internal/httpclient"
	"github.com/ferro-labs/ai-gateway/providers/core"
	"github.com/ferro-labs/ai-gateway/providers/internal/openaicompat"
)

const (
	// Name is the canonical provider identifier.
	Name           = "ollama-cloud"
	defaultBaseURL = "https://ollama.com"
)

var defaultModels = []string{
	"gpt-oss:120b",
	"gpt-oss:20b",
	"qwen3-coder:480b",
	"deepseek-v3.1:671b",
}

// Provider implements the Ollama Cloud API client.
type Provider struct {
	name       string
	apiKey     string
	baseURL    string
	httpClient *http.Client

	mu         sync.RWMutex
	models     []string
	discovered []string
}

// Compile-time interface assertions.
var (
	_ core.Provider          = (*Provider)(nil)
	_ core.StreamProvider    = (*Provider)(nil)
	_ core.EmbeddingProvider = (*Provider)(nil)
	_ core.DiscoveryProvider = (*Provider)(nil)
)

// New creates a new Ollama Cloud provider.
func New(apiKey, baseURL string, models []string) (*Provider, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("ollama-cloud: api key is required")
	}

	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	if err := core.ValidateBaseURL(Name, baseURL); err != nil {
		return nil, err
	}

	models = normalizeModels(models)
	if len(models) == 0 {
		models = append([]string(nil), defaultModels...)
	}

	return &Provider{
		name:       Name,
		apiKey:     apiKey,
		baseURL:    baseURL,
		httpClient: providerhttp.ForProvider(Name),
		models:     models,
	}, nil
}

// Name implements core.Provider.
func (p *Provider) Name() string { return p.name }

// SupportedModels returns the configured and discovered models.
func (p *Provider) SupportedModels() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return combineModels(p.models, p.discovered)
}

// SupportsModel returns true if model is configured or has been discovered.
func (p *Provider) SupportsModel(model string) bool {
	model = strings.TrimSpace(model)
	model = strings.TrimPrefix(model, Name+"/")
	if model == "" {
		return false
	}

	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, supported := range combineModels(p.models, p.discovered) {
		if model == supported {
			return true
		}
	}
	return false
}

// Models returns structured model metadata.
func (p *Provider) Models() []core.ModelInfo {
	return core.ModelsFromList(p.name, p.SupportedModels())
}

// chatParams builds the shared OpenAI-compatible chat endpoint configuration.
// Ollama Cloud exposes an OpenAI-compatible surface at https://ollama.com/v1,
// so chat and streaming route through the shared openaicompat helpers (which
// forward request params directly and normalize the OpenAI response shape).
// Embeddings stay on the native /api/embed path — see embedding.go.
func (p *Provider) chatParams() openaicompat.ChatParams {
	return openaicompat.ChatParams{
		HTTPClient: p.httpClient,
		URL:        p.baseURL + "/v1/chat/completions",
		Provider:   p.name,
		Label:      "ollama cloud",
		Headers: map[string]string{
			"Authorization": "Bearer " + p.apiKey,
			"Content-Type":  "application/json",
		},
	}
}

// Complete sends a non-streaming chat request to Ollama Cloud's
// OpenAI-compatible /v1/chat/completions endpoint.
func (p *Provider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	return openaicompat.PostChat(ctx, p.chatParams(), req)
}

// CompleteStream sends a streaming chat request to Ollama Cloud's
// OpenAI-compatible /v1/chat/completions endpoint.
func (p *Provider) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
	return openaicompat.PostStream(ctx, p.chatParams(), req)
}

// DiscoverModels fetches the live Ollama Cloud model list from the
// OpenAI-compatible /v1/models endpoint and caches the discovered IDs so
// SupportsModel recognizes them.
func (p *Provider) DiscoverModels(ctx context.Context) ([]core.ModelInfo, error) {
	models, err := discov.DiscoverOpenAICompatibleModels(ctx, p.httpClient, p.baseURL+"/v1/models", p.apiKey, p.name)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]struct{}, len(models))
	modelIDs := make([]string, 0, len(models))
	deduped := make([]core.ModelInfo, 0, len(models))
	for _, m := range models {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		modelIDs = append(modelIDs, id)
		deduped = append(deduped, m)
	}

	p.mu.Lock()
	p.discovered = modelIDs
	p.mu.Unlock()

	return deduped, nil
}

// setHeaders applies the shared auth and content-type headers. Retained for the
// native /api/embed embedding path — see embedding.go.
func (p *Provider) setHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	req.Header.Set("Content-Type", "application/json")
}

func apiError(statusCode int, body []byte) error {
	msg := parseErrorMessage(body)
	if msg == "" {
		msg = http.StatusText(statusCode)
	}
	if msg == "" {
		msg = "unexpected response"
	}
	return fmt.Errorf("ollama-cloud API error (%d): %s", statusCode, msg)
}

func parseErrorMessage(body []byte) string {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return ""
	}

	var envelope struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil {
		if len(envelope.Error) > 0 && string(envelope.Error) != "null" {
			var errString string
			if err := json.Unmarshal(envelope.Error, &errString); err == nil {
				return errString
			}
			var errObject struct {
				Message string `json:"message"`
				Type    string `json:"type"`
				Code    string `json:"code"`
			}
			if err := json.Unmarshal(envelope.Error, &errObject); err == nil {
				if errObject.Message != "" {
					return errObject.Message
				}
				if errObject.Type != "" {
					return errObject.Type
				}
				if errObject.Code != "" {
					return errObject.Code
				}
			}
		}
		if envelope.Message != "" {
			return envelope.Message
		}
	}

	msg := string(body)
	if len(msg) > 4096 {
		msg = msg[:4096]
	}
	return msg
}

func normalizeModels(models []string) []string {
	out := make([]string, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		out = append(out, model)
	}
	return out
}

func combineModels(primary, secondary []string) []string {
	out := make([]string, 0, len(primary)+len(secondary))
	seen := make(map[string]struct{}, len(primary)+len(secondary))
	for _, model := range primary {
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		out = append(out, model)
	}
	for _, model := range secondary {
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		out = append(out, model)
	}
	return out
}
