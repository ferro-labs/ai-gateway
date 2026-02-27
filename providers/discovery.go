package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// openAIModelList mirrors the OpenAI /v1/models response schema.
type openAIModelList struct {
	Object string `json:"object"`
	Data   []struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	} `json:"data"`
}

// discoverOpenAICompatibleModels fetches a live model list from any provider
// that exposes an OpenAI-compatible GET /v1/models (or similar) endpoint.
func discoverOpenAICompatibleModels(ctx context.Context, client *http.Client, url, apiKey, providerName string) ([]ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create discovery request: %w", err)
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
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

	var list openAIModelList
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("failed to parse model list: %w", err)
	}

	models := make([]ModelInfo, 0, len(list.Data))
	for _, m := range list.Data {
		ownedBy := m.OwnedBy
		if ownedBy == "" {
			ownedBy = providerName
		}
		models = append(models, ModelInfo{
			ID:      m.ID,
			Object:  "model",
			Created: m.Created,
			OwnedBy: ownedBy,
		})
	}
	return models, nil
}
