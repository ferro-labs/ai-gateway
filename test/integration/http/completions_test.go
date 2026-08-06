//go:build integration
// +build integration

package http_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// postCompletions sends a legacy completions body through the wired router.
func postCompletions(t *testing.T, env *testEnv, body string) *http.Response {
	t.Helper()

	req := newTestRequest(t, http.MethodPost, env.Server.URL+"/v1/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testMasterKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/completions: %v", err)
	}
	t.Cleanup(func() { closeTestBody(t, resp.Body) })
	return resp
}

// The legacy surface is routed through the same Gateway entry point as
// /v1/chat/completions and the chat response is re-wrapped in the legacy
// envelope: a text choice, no message object, and the chat usage carried over.
func TestCompletions_WrapsChatResponseInLegacyEnvelope(t *testing.T) {
	env := newTestServer(t)

	//nolint:bodyclose // postCompletions registers the close with t.Cleanup.
	resp := postCompletions(t, env, `{"model":"`+stubModelName+`","prompt":"Hello"}`)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Object  string `json:"object"`
		Model   string `json:"model"`
		Choices []struct {
			Text         string `json:"text"`
			Index        int    `json:"index"`
			Logprobs     any    `json:"logprobs"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if result.Object != "text_completion" {
		t.Errorf("object = %q, want text_completion", result.Object)
	}
	if result.Model != stubModelName {
		t.Errorf("model = %q, want %q", result.Model, stubModelName)
	}
	if len(result.Choices) == 0 {
		t.Fatal("expected at least one choice")
	}
	if result.Choices[0].Text == "" {
		t.Error("choices[0].text is empty; the chat content was not carried over")
	}
	if result.Choices[0].FinishReason != "stop" {
		t.Errorf("finish_reason = %q, want stop", result.Choices[0].FinishReason)
	}
	if result.Choices[0].Logprobs != nil {
		t.Errorf("logprobs = %v, want explicit null", result.Choices[0].Logprobs)
	}
	if result.Usage.TotalTokens == 0 {
		t.Error("usage was not carried over from the chat response")
	}
}

// A single-element array prompt is representable as one chat message and is
// accepted; the shapes the chat surface cannot express are refused with a 400
// rather than forwarded opaquely upstream.
func TestCompletions_PromptShapes(t *testing.T) {
	env := newTestServer(t)

	for _, tc := range []struct {
		name       string
		prompt     string
		wantStatus int
		wantCode   string
	}{
		{"string", `"Hello"`, http.StatusOK, ""},
		{"single-element array", `["Hello"]`, http.StatusOK, ""},
		{"batch array", `["one","two"]`, http.StatusBadRequest, "unsupported_parameter"},
		{"token ids", `[1,2,3]`, http.StatusBadRequest, "unsupported_parameter"},
		{"nested token ids", `[[1,2],[3,4]]`, http.StatusBadRequest, "unsupported_parameter"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			//nolint:bodyclose // postCompletions registers the close with t.Cleanup.
			resp := postCompletions(t, env, `{"model":"`+stubModelName+`","prompt":`+tc.prompt+`}`)
			if resp.StatusCode != tc.wantStatus {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("status = %d, want %d: %s", resp.StatusCode, tc.wantStatus, body)
			}
			if tc.wantCode != "" {
				assertOpenAIError(t, resp.Body, "invalid_request_error", tc.wantCode)
			}
		})
	}
}

// Streaming has no legacy envelope to stream into, so it is refused explicitly
// rather than silently answered as a single non-streamed body.
func TestCompletions_StreamIsRefused(t *testing.T) {
	env := newTestServer(t)

	//nolint:bodyclose // postCompletions registers the close with t.Cleanup.
	resp := postCompletions(t, env, `{"model":"`+stubModelName+`","prompt":"Hello","stream":true}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	assertOpenAIError(t, resp.Body, "invalid_request_error", "streaming_not_supported")
}

// Admission is the Gateway's, so an unroutable model gets the same 404
// model_not_found the chat, embeddings and image surfaces give — not the 400
// that reading the wider registered-provider set produced.
func TestCompletions_UnknownModelIs404(t *testing.T) {
	env := newTestServer(t)

	//nolint:bodyclose // postCompletions registers the close with t.Cleanup.
	resp := postCompletions(t, env, `{"model":"no-such-model","prompt":"Hello"}`)
	if resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 404: %s", resp.StatusCode, body)
	}
	assertOpenAIError(t, resp.Body, "invalid_request_error", "model_not_found")
}

// A missing model is the client's mistake, and must not reach routing.
func TestCompletions_MissingModelIs400(t *testing.T) {
	env := newTestServer(t)

	//nolint:bodyclose // postCompletions registers the close with t.Cleanup.
	resp := postCompletions(t, env, `{"prompt":"Hello"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	assertOpenAIError(t, resp.Body, "invalid_request_error", "invalid_request")
}

// The route is behind the same auth as its siblings.
func TestCompletions_RequiresAuth(t *testing.T) {
	env := newTestServer(t)

	req := newTestRequest(t, http.MethodPost, env.Server.URL+"/v1/completions",
		strings.NewReader(`{"model":"`+stubModelName+`","prompt":"Hello"}`))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/completions: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}
