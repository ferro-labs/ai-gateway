//go:build integration
// +build integration

package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

func TestChat_NonStreaming_Success(t *testing.T) {
	env := newTestServer(t)

	body := `{
		"model": "` + stubModelName + `",
		"messages": [{"role": "user", "content": "Hello"}]
	}`

	req := newTestRequest(t, "POST", env.Server.URL+"/v1/chat/completions", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+testMasterKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/chat/completions: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, respBody)
	}

	var result struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Model   string `json:"model"`
		Choices []struct {
			Index   int `json:"index"`
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if result.Object != "chat.completion" {
		t.Errorf("expected object=chat.completion, got %q", result.Object)
	}
	if len(result.Choices) == 0 {
		t.Fatal("expected at least one choice")
	}
	if result.Choices[0].Message.Role != "assistant" {
		t.Errorf("expected role=assistant, got %q", result.Choices[0].Message.Role)
	}
	if result.Choices[0].Message.Content == "" {
		t.Error("expected non-empty content")
	}
	if result.Choices[0].FinishReason != "stop" {
		t.Errorf("expected finish_reason=stop, got %q", result.Choices[0].FinishReason)
	}
	if result.Usage.TotalTokens == 0 {
		t.Error("expected non-zero usage")
	}
}

func TestChat_NonStreaming_UpstreamError(t *testing.T) {
	env := newTestServer(t)

	// Override the stub to return an error.
	env.Stub.CompleteHook = func(_ context.Context, _ core.Request) (*core.Response, error) {
		return nil, fmt.Errorf("upstream provider error: internal server error")
	}

	body := `{
		"model": "` + stubModelName + `",
		"messages": [{"role": "user", "content": "Hello"}]
	}`

	req := newTestRequest(t, "POST", env.Server.URL+"/v1/chat/completions", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+testMasterKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	// Should map to an OpenAI error envelope, not a bare 500.
	if resp.StatusCode < 400 {
		t.Fatalf("expected error status, got %d", resp.StatusCode)
	}

	var errResp struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if errResp.Error.Message == "" {
		t.Error("expected non-empty error message")
	}
	if errResp.Error.Type == "" {
		t.Error("expected non-empty error type")
	}
}

func TestChat_InvalidJSON_Returns400(t *testing.T) {
	env := newTestServer(t)

	req := newTestRequest(t, "POST", env.Server.URL+"/v1/chat/completions", bytes.NewBufferString(`{invalid json`))
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

	var errResp struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if errResp.Error.Type != "invalid_request_error" {
		t.Errorf("expected type=invalid_request_error, got %q", errResp.Error.Type)
	}
}

func TestChat_MissingModel_Returns400(t *testing.T) {
	env := newTestServer(t)

	body := `{
		"messages": [{"role": "user", "content": "Hello"}]
	}`

	req := newTestRequest(t, "POST", env.Server.URL+"/v1/chat/completions", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+testMasterKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 400, got %d: %s", resp.StatusCode, body)
	}
}

func TestChat_UnknownModel_Returns400(t *testing.T) {
	env := newTestServer(t)

	body := `{
		"model": "nonexistent-model-xyz",
		"messages": [{"role": "user", "content": "Hello"}]
	}`

	req := newTestRequest(t, "POST", env.Server.URL+"/v1/chat/completions", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+testMasterKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 400, got %d: %s", resp.StatusCode, body)
	}
}
