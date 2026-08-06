//go:build integration
// +build integration

package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

func TestRerank_Success(t *testing.T) {
	env := newTestServer(t)

	body := `{
		"model": "` + stubRerankModel + `",
		"query": "what is the capital?",
		"documents": ["Carson City", "Washington DC"],
		"top_n": 2
	}`

	req := newTestRequest(t, "POST", env.Server.URL+"/v1/rerank", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+testMasterKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/rerank: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, respBody)
	}

	var result struct {
		Results []struct {
			Index          int     `json:"index"`
			RelevanceScore float64 `json:"relevance_score"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result.Results))
	}
	if result.Results[0].RelevanceScore < result.Results[1].RelevanceScore {
		t.Errorf("results not ordered by descending relevance: %+v", result.Results)
	}
}

func TestRerank_MissingQuery_Returns400(t *testing.T) {
	env := newTestServer(t)

	body := `{"model":"` + stubRerankModel + `","documents":["a","b"]}`
	req := newTestRequest(t, "POST", env.Server.URL+"/v1/rerank", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+testMasterKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

// TestRerank_CapabilityMiss_Returns404 guards that a model no registered
// RerankProvider serves returns 404 invalid_request_error/model_not_found, not 500.
func TestRerank_CapabilityMiss_Returns404(t *testing.T) {
	env := newTestServer(t)

	body := `{"model":"no-such-rerank-model","query":"q","documents":["a"]}`
	req := newTestRequest(t, "POST", env.Server.URL+"/v1/rerank", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+testMasterKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	if resp.StatusCode != http.StatusNotFound {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 404, got %d: %s", resp.StatusCode, b)
	}
	assertOpenAIError(t, resp.Body, "invalid_request_error", "model_not_found")
}

// TestRerank_UpstreamError_Returns500 verifies a generic provider failure maps
// to 500, distinct from the capability-miss 404 path.
func TestRerank_UpstreamError_Returns500(t *testing.T) {
	env := newTestServer(t)
	env.Stub.RerankHook = func(_ context.Context, _ core.RerankRequest) (*core.RerankResponse, error) {
		return nil, errors.New("upstream rerank API error (503)")
	}

	body := `{"model":"` + stubRerankModel + `","query":"q","documents":["a"]}`
	req := newTestRequest(t, "POST", env.Server.URL+"/v1/rerank", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+testMasterKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	if resp.StatusCode != http.StatusInternalServerError {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 500, got %d: %s", resp.StatusCode, b)
	}
}

func TestRerank_RequiresAuth(t *testing.T) {
	env := newTestServer(t)

	body := `{"model":"` + stubRerankModel + `","query":"q","documents":["a"]}`
	req := newTestRequest(t, "POST", env.Server.URL+"/v1/rerank", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}
