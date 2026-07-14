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

// postImages is a small helper that POSTs an /v1/images/generations body with
// the master-key bearer and returns the response.
func postImages(t *testing.T, url, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", url+"/v1/images/generations", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+testMasterKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/images/generations: %v", err)
	}
	return resp
}

func TestImages_Success(t *testing.T) {
	env := newTestServer(t)

	resp := postImages(t, env.Server.URL, `{"model":"`+stubImageModel+`","prompt":"a red bicycle"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, b)
	}

	var result core.ImageResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Data) == 0 {
		t.Fatal("expected at least one image in data")
	}
	if result.Data[0].B64JSON == "" && result.Data[0].URL == "" {
		t.Error("expected b64_json or url to be populated")
	}
}

// TestImages_CapabilityMiss_Returns404 is the key regression guard for #149/#154
// at the full HTTP stack: a model with no registered ImageProvider must map to
// 404 invalid_request_error/model_not_found, NOT 500.
func TestImages_CapabilityMiss_Returns404(t *testing.T) {
	env := newTestServer(t)

	resp := postImages(t, env.Server.URL, `{"model":"no-such-image-model","prompt":"a cat"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 404, got %d: %s", resp.StatusCode, b)
	}
	assertOpenAIError(t, resp.Body, "invalid_request_error", "model_not_found")
}

func TestImages_MissingModel_Returns400(t *testing.T) {
	env := newTestServer(t)

	resp := postImages(t, env.Server.URL, `{"prompt":"a cat"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestImages_MissingPrompt_Returns400(t *testing.T) {
	env := newTestServer(t)

	resp := postImages(t, env.Server.URL, `{"model":"`+stubImageModel+`"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestImages_RequiresAuth(t *testing.T) {
	env := newTestServer(t)

	req, _ := http.NewRequest("POST", env.Server.URL+"/v1/images/generations",
		bytes.NewBufferString(`{"model":"`+stubImageModel+`","prompt":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/images/generations: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

// TestImages_UpstreamError_Returns500 verifies a generic provider failure (not a
// capability miss) maps to 500 server_error, distinguishing it from the 404 path.
func TestImages_UpstreamError_Returns500(t *testing.T) {
	env := newTestServer(t)
	env.Stub.GenerateImageHook = func(_ context.Context, _ core.ImageRequest) (*core.ImageResponse, error) {
		return nil, errors.New("bedrock InvokeModel: throttled")
	}

	resp := postImages(t, env.Server.URL, `{"model":"`+stubImageModel+`","prompt":"a cat"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 500, got %d: %s", resp.StatusCode, b)
	}
}
