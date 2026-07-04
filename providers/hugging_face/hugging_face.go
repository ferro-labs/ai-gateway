// Package huggingface provides a client for the Hugging Face Inference API.
package huggingface

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/ferro-labs/ai-gateway/internal/discovery"
	providerhttp "github.com/ferro-labs/ai-gateway/internal/httpclient"
	"github.com/ferro-labs/ai-gateway/providers/core"
	"github.com/ferro-labs/ai-gateway/providers/internal/openaicompat"
)

// Name is the canonical provider identifier.
const Name = "hugging-face"

// defaultBaseURL is the Inference Providers router. Chat and model discovery are
// OpenAI-compatible under /v1; task-specific routes (feature extraction,
// text-to-image) live directly under the router root.
const defaultBaseURL = "https://router.huggingface.co/v1"

// Provider implements the Hugging Face Inference API client.
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
	_ core.ImageProvider     = (*Provider)(nil)
	_ core.DiscoveryProvider = (*Provider)(nil)
	_ core.ProxiableProvider = (*Provider)(nil)
)

// New creates a new Hugging Face provider.
// If baseURL is empty, the shared Inference Providers router is used.
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

// routerRoot returns the Inference Providers router root by stripping the
// trailing "/v1" chat suffix from baseURL. Task-specific routes (feature
// extraction, text-to-image) hang directly off the root, not under /v1.
func (p *Provider) routerRoot() string {
	return strings.TrimSuffix(p.baseURL, "/v1")
}

// escapeModelPath percent-escapes each segment of a caller-supplied model id
// (e.g. "owner/name") while preserving the "/" separators the router task routes
// use. Empty and dot ("."/"..") segments are rejected — url.PathEscape leaves
// them unchanged, so they would otherwise let a crafted model traverse to a
// different router path.
func escapeModelPath(model string) (string, error) {
	parts := strings.Split(model, "/")
	for i, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("hugging face: invalid model path segment %q", part)
		}
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/"), nil
}

// AuthHeaders implements core.ProxiableProvider.
func (p *Provider) AuthHeaders() map[string]string {
	return map[string]string{"Authorization": "Bearer " + p.apiKey}
}

// SupportedModels returns known Hugging Face model examples.
func (p *Provider) SupportedModels() []string {
	return []string{
		"meta-llama/Meta-Llama-3.1-8B-Instruct",
		"mistralai/Mistral-7B-Instruct-v0.3",
		"Qwen/Qwen2.5-72B-Instruct",
	}
}

// SupportsModel returns true for any model — Hugging Face validates model IDs.
func (p *Provider) SupportsModel(_ string) bool { return true }

// Models returns structured model metadata.
func (p *Provider) Models() []core.ModelInfo {
	return core.ModelsFromList(p.name, p.SupportedModels())
}

// DiscoverModels fetches the live model list from the Hugging Face /models endpoint.
func (p *Provider) DiscoverModels(ctx context.Context) ([]core.ModelInfo, error) {
	return discovery.DiscoverOpenAICompatibleModels(ctx, p.httpClient, p.baseURL+"/models", p.apiKey, p.name)
}

// Complete sends a chat completion request to Hugging Face.
func (p *Provider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	return openaicompat.PostChat(ctx, openaicompat.ChatParams{
		HTTPClient: p.httpClient,
		URL:        p.baseURL + "/chat/completions",
		Provider:   p.name,
		Label:      "hugging face",
		Headers:    map[string]string{"Authorization": "Bearer " + p.apiKey, "Content-Type": "application/json"},
	}, req)
}

// CompleteStream sends a streaming chat completion request to Hugging Face.
func (p *Provider) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
	return openaicompat.PostStream(ctx, openaicompat.ChatParams{
		HTTPClient: p.httpClient,
		URL:        p.baseURL + "/chat/completions",
		Provider:   p.name,
		Label:      "hugging face",
		Headers:    map[string]string{"Authorization": "Bearer " + p.apiKey, "Content-Type": "application/json"},
	}, req)
}

// postTask sends a POST with a JSON body to a task-specific Hugging Face router
// endpoint and returns the raw response bytes. Non-200 responses are translated
// into a core.APIError carrying the upstream status and message.
func (p *Provider) postTask(ctx context.Context, url string, body io.Reader) ([]byte, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	if httpResp.StatusCode != http.StatusOK {
		return nil, core.APIError("hugging face", httpResp.StatusCode, respBody)
	}
	return respBody, nil
}

// Embed sends a feature-extraction request to Hugging Face. The task API is not
// OpenAI-shaped: it takes {"inputs": <string|[]string>} and returns a bare JSON
// array of float vectors ([]float64 for a single input, [][]float64 for a
// batch). Hugging Face does not report token usage, so Usage stays zero;
// req.EncodingFormat and req.Dimensions have no task-API equivalent and are ignored.
func (p *Provider) Embed(ctx context.Context, req core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	escaped, err := escapeModelPath(req.Model)
	if err != nil {
		return nil, err
	}
	bodyReader, _, release, err := core.JSONBodyReader(map[string]any{"inputs": req.Input})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal embedding request: %w", err)
	}
	defer release()

	taskURL := p.routerRoot() + "/hf-inference/models/" + escaped + "/pipeline/feature-extraction"
	respBody, err := p.postTask(ctx, taskURL, bodyReader)
	if err != nil {
		return nil, err
	}

	vectors, err := parseFeatureExtraction(respBody)
	if err != nil {
		return nil, err
	}
	if want := expectedEmbeddingCount(req.Input); want > 0 && len(vectors) != want {
		return nil, fmt.Errorf("hugging face: got %d embedding vectors for %d inputs", len(vectors), want)
	}
	data := make([]core.Embedding, len(vectors))
	for i, vec := range vectors {
		data[i] = core.Embedding{Object: "embedding", Embedding: vec, Index: i}
	}
	return &core.EmbeddingResponse{Object: "list", Data: data, Model: req.Model}, nil
}

// expectedEmbeddingCount reports how many input strings an embedding request
// carries (1 for a single string), or 0 when the shape is unknown and the count
// guard should be skipped.
func expectedEmbeddingCount(input any) int {
	switch v := input.(type) {
	case string:
		return 1
	case []string:
		return len(v)
	case []any:
		return len(v)
	default:
		return 0
	}
}

// parseFeatureExtraction decodes a Hugging Face feature-extraction response into
// one vector per input. The response is a bare JSON array: []float64 for a
// single string input, or [][]float64 for a batch of inputs.
func parseFeatureExtraction(body []byte) ([][]float64, error) {
	var batch [][]float64
	if err := json.Unmarshal(body, &batch); err == nil {
		return batch, nil
	}
	var single []float64
	if err := json.Unmarshal(body, &single); err == nil {
		return [][]float64{single}, nil
	}
	return nil, fmt.Errorf("failed to parse feature-extraction response")
}

// GenerateImage sends a text-to-image request to Hugging Face. The task API is
// not OpenAI-shaped: it takes {"inputs": <prompt>, "parameters": {...}} and
// returns the generated image as raw bytes, which are base64-encoded into the
// OpenAI-style b64_json field.
func (p *Provider) GenerateImage(ctx context.Context, req core.ImageRequest) (*core.ImageResponse, error) {
	if req.N != nil && *req.N != 1 {
		return nil, fmt.Errorf("hugging face: text-to-image returns one image per request; only n=1 is supported (got %d)", *req.N)
	}
	escaped, err := escapeModelPath(req.Model)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{"inputs": req.Prompt}
	if params := imageParameters(req); len(params) > 0 {
		payload["parameters"] = params
	}
	bodyReader, _, release, err := core.JSONBodyReader(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal image request: %w", err)
	}
	defer release()

	taskURL := p.routerRoot() + "/hf-inference/models/" + escaped
	respBody, err := p.postTask(ctx, taskURL, bodyReader)
	if err != nil {
		return nil, err
	}

	b64 := base64.StdEncoding.EncodeToString(respBody)
	return &core.ImageResponse{Data: []core.GeneratedImage{{B64JSON: b64}}}, nil
}

// imageParameters builds the Hugging Face text-to-image "parameters" object from
// the OpenAI-shaped request. Only fields with a Hugging Face equivalent are
// mapped; Size ("WIDTHxHEIGHT") becomes width/height integers.
func imageParameters(req core.ImageRequest) map[string]any {
	params := map[string]any{}
	if w, h, ok := parseSize(req.Size); ok {
		params["width"] = w
		params["height"] = h
	}
	return params
}

// parseSize splits an OpenAI-style "WIDTHxHEIGHT" size string into positive
// integer dimensions. It reports ok=false for empty or malformed values.
func parseSize(size string) (width, height int, ok bool) {
	parts := strings.SplitN(size, "x", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	w, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	h, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil || w <= 0 || h <= 0 {
		return 0, 0, false
	}
	return w, h, true
}
