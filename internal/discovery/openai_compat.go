// Package discovery provides shared helpers for providers that support live
// model enumeration via OpenAI-compatible GET /v1/models (or similar) endpoints.
package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// modelItem mirrors a single entry in an OpenAI-compatible /models response.
type modelItem struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// openAIModelList mirrors the wrapped OpenAI /v1/models response schema.
type openAIModelList struct {
	Object string      `json:"object"`
	Data   []modelItem `json:"data"`
}

// parseModelItems decodes an OpenAI-compatible model list. It tolerates both the
// standard wrapped shape ({"object":...,"data":[...]}) and a bare JSON array
// ([...]) returned by some providers (e.g. Together AI).
func parseModelItems(body []byte) ([]modelItem, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var items []modelItem
		if err := json.Unmarshal(trimmed, &items); err != nil {
			return nil, fmt.Errorf("failed to parse model list: %w", err)
		}
		return items, nil
	}

	var list openAIModelList
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("failed to parse model list: %w", err)
	}
	return list.Data, nil
}

// DiscoverModelsWithHeaders fetches a live model list from any provider that
// exposes an OpenAI-compatible GET /v1/models (or similar) endpoint, setting the
// supplied headers on the request. It tolerates both the wrapped {"data":[...]}
// shape and a bare JSON array. Items missing owned_by fall back to providerName.
func DiscoverModelsWithHeaders(ctx context.Context, client *http.Client, url string, headers map[string]string, providerName string) ([]core.ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create discovery request: %w", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("discovery request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read discovery response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("discovery request returned %d: %s", resp.StatusCode, string(body))
	}

	items, err := parseModelItems(body)
	if err != nil {
		return nil, err
	}

	models := make([]core.ModelInfo, 0, len(items))
	for _, m := range items {
		ownedBy := m.OwnedBy
		if ownedBy == "" {
			ownedBy = providerName
		}
		models = append(models, core.ModelInfo{
			ID:      m.ID,
			Object:  "model",
			Created: m.Created,
			OwnedBy: ownedBy,
		})
	}
	return models, nil
}

// DiscoverOpenAICompatibleModels fetches a live model list from any provider
// that exposes an OpenAI-compatible GET /v1/models (or similar) endpoint using
// Bearer authentication. If apiKey is empty the Authorization header is omitted
// (for unauthenticated endpoints such as local Ollama instances).
func DiscoverOpenAICompatibleModels(ctx context.Context, client *http.Client, url, apiKey, providerName string) ([]core.ModelInfo, error) {
	var headers map[string]string
	if apiKey != "" {
		headers = map[string]string{"Authorization": "Bearer " + apiKey}
	}
	return DiscoverModelsWithHeaders(ctx, client, url, headers, providerName)
}
