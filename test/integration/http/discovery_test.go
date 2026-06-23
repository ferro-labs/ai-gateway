//go:build integration
// +build integration

package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// fetchModelIDs GETs /v1/models and returns the set of model IDs.
func fetchModelIDs(t *testing.T, url string) map[string]bool {
	t.Helper()
	req, _ := http.NewRequest("GET", url+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+testMasterKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /v1/models: %v", err)
	}
	defer resp.Body.Close()

	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	ids := make(map[string]bool, len(result.Data))
	for _, m := range result.Data {
		ids[m.ID] = true
	}
	return ids
}

// TestDiscovery_OverridesModelsList verifies the #146 precedence at the HTTP
// layer: once live discovery succeeds for a provider, /v1/models reflects the
// discovered list instead of the provider's hardcoded SupportedModels().
func TestDiscovery_OverridesModelsList(t *testing.T) {
	env := newTestServer(t)

	const discovered = "stub-discovered-model"
	env.Stub.DiscoverModelsHook = func(_ context.Context) ([]core.ModelInfo, error) {
		return []core.ModelInfo{{ID: discovered, Object: "model", OwnedBy: "stub"}}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Long interval: only the immediate first refresh runs during the test.
	if err := env.Gateway.StartDiscovery(ctx, time.Hour); err != nil {
		t.Fatalf("StartDiscovery: %v", err)
	}

	// The first refresh runs asynchronously; poll until it lands.
	deadline := time.Now().Add(3 * time.Second)
	for {
		ids := fetchModelIDs(t, env.Server.URL)
		if ids[discovered] {
			// Discovered list must REPLACE the hardcoded models for this provider.
			if ids[stubModelName] {
				t.Errorf("hardcoded model %q still present after discovery; expected replacement", stubModelName)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("discovered model %q did not appear in /v1/models within timeout", discovered)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
