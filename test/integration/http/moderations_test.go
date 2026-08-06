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

func TestModerations_Success(t *testing.T) {
	env := newTestServer(t)

	body := `{"model":"` + stubModerationModel + `","input":"is this ok?"}`
	req := newTestRequest(t, "POST", env.Server.URL+"/v1/moderations", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+testMasterKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/moderations: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, b)
	}

	var result struct {
		Results []struct {
			Flagged    bool            `json:"flagged"`
			Categories map[string]bool `json:"categories"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result.Results))
	}
}

func TestModerations_MissingModel_Returns400(t *testing.T) {
	env := newTestServer(t)

	req := newTestRequest(t, "POST", env.Server.URL+"/v1/moderations", bytes.NewBufferString(`{"input":"hi"}`))
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

// TestModerations_CapabilityMiss_Returns404 guards that a model no registered
// ModerationProvider serves returns 404, not 500.
func TestModerations_CapabilityMiss_Returns404(t *testing.T) {
	env := newTestServer(t)

	body := `{"model":"no-such-moderation-model","input":"hi"}`
	req := newTestRequest(t, "POST", env.Server.URL+"/v1/moderations", bytes.NewBufferString(body))
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

func TestModerations_UpstreamError_Returns500(t *testing.T) {
	env := newTestServer(t)
	env.Stub.ModerateHook = func(_ context.Context, _ core.ModerationRequest) (*core.ModerationResponse, error) {
		return nil, errors.New("upstream moderation API error (503)")
	}

	body := `{"model":"` + stubModerationModel + `","input":"hi"}`
	req := newTestRequest(t, "POST", env.Server.URL+"/v1/moderations", bytes.NewBufferString(body))
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

func TestModerations_RequiresAuth(t *testing.T) {
	env := newTestServer(t)

	body := `{"model":"` + stubModerationModel + `","input":"hi"}`
	req := newTestRequest(t, "POST", env.Server.URL+"/v1/moderations", bytes.NewBufferString(body))
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
