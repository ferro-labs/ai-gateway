//go:build integration
// +build integration

package http_test

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func TestModels_ListStubModels(t *testing.T) {
	env := newTestServer(t)

	req := newTestRequest(t, "GET", env.Server.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+testMasterKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /v1/models: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Object string `json:"object"`
		Data   []struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if result.Object != "list" {
		t.Fatalf("expected object=list, got %q", result.Object)
	}

	if len(result.Data) < 3 {
		t.Fatalf("expected at least 3 models (stub has 3), got %d", len(result.Data))
	}

	// Verify all stub models are present.
	modelIDs := make(map[string]bool)
	for _, m := range result.Data {
		modelIDs[m.ID] = true
		if m.Object != "model" {
			t.Errorf("model %s: expected object=model, got %q", m.ID, m.Object)
		}
	}
	for _, expected := range []string{stubModelName, stubModelName2, stubEmbedModel} {
		if !modelIDs[expected] {
			t.Errorf("expected model %q in response, not found", expected)
		}
	}
}

func TestModels_RequiresAuth(t *testing.T) {
	env := newTestServer(t)

	resp, err := http.Get(env.Server.URL + "/v1/models")
	if err != nil {
		t.Fatalf("GET /v1/models: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}
