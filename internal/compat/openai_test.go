package compat

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

func newTestClient(serverURL string) *Client {
	return New("test-provider", "test-key", serverURL, "https://example.com/v1", http.DefaultClient)
}

func TestClient_Name(t *testing.T) {
	c := newTestClient("https://example.com")
	if c.Name() != "test-provider" {
		t.Errorf("Name() = %q, want test-provider", c.Name())
	}
}

func TestClient_BaseURL_TrimsTrailingSlash(t *testing.T) {
	c := New("p", "k", "https://example.com/v1/", "", http.DefaultClient)
	if strings.HasSuffix(c.BaseURL(), "/") {
		t.Errorf("BaseURL() should have no trailing slash, got %q", c.BaseURL())
	}
}

func TestClient_DefaultBaseURL(t *testing.T) {
	c := New("p", "k", "", "https://default.example.com/v1", http.DefaultClient)
	if c.BaseURL() != "https://default.example.com/v1" {
		t.Errorf("BaseURL() = %q, want default", c.BaseURL())
	}
}

func TestClient_AuthHeaders(t *testing.T) {
	c := newTestClient("https://example.com")
	headers := c.AuthHeaders()
	if headers["Authorization"] != "Bearer test-key" {
		t.Errorf("AuthHeaders[Authorization] = %q, want Bearer test-key", headers["Authorization"])
	}
}

func TestClient_Complete(t *testing.T) {
	respBody := `{"id":"cmpl-1","object":"chat.completion","created":1234,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`

	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, respBody)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	resp, err := c.Complete(context.Background(), core.Request{
		Model:    "m",
		Messages: []core.Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if resp.Provider != "test-provider" {
		t.Errorf("Provider = %q, want test-provider", resp.Provider)
	}
	if resp.ID != "cmpl-1" {
		t.Errorf("ID = %q, want cmpl-1", resp.ID)
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Message.Content != "hi" {
		t.Errorf("unexpected choices: %+v", resp.Choices)
	}

	// stream must be false in the serialised body
	var reqMap map[string]any
	if err := json.Unmarshal(capturedBody, &reqMap); err != nil {
		t.Fatalf("unmarshal captured body: %v", err)
	}
	if v, ok := reqMap["stream"]; ok && v != false {
		t.Errorf("expected stream=false or absent, got %v", v)
	}
}

func TestClient_Complete_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"invalid api key","type":"auth_error"}}`)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	_, err := c.Complete(context.Background(), core.Request{
		Model:    "m",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error for 401 response")
	}
	if !strings.Contains(err.Error(), "invalid api key") {
		t.Errorf("error %q should contain API message", err.Error())
	}
}

func TestClient_CompleteStream(t *testing.T) {
	sseData := "data: {\"id\":\"s1\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"s1\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"s1\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"

	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sseData)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	ch, err := c.CompleteStream(context.Background(), core.Request{
		Model:    "m",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("CompleteStream() error: %v", err)
	}

	var chunks []core.StreamChunk
	for chunk := range ch {
		if chunk.Error != nil {
			t.Fatalf("stream error: %v", chunk.Error)
		}
		chunks = append(chunks, chunk)
	}

	if len(chunks) < 3 {
		t.Fatalf("expected >= 3 chunks, got %d", len(chunks))
	}
	if chunks[1].Choices[0].Delta.Content != "hello" {
		t.Errorf("delta content = %q, want hello", chunks[1].Choices[0].Delta.Content)
	}

	// stream must be true in the serialised body
	var reqMap map[string]any
	if err := json.Unmarshal(capturedBody, &reqMap); err != nil {
		t.Fatalf("unmarshal captured body: %v", err)
	}
	if v := reqMap["stream"]; v != true {
		t.Errorf("expected stream=true in request body, got %v", v)
	}
}

func TestClient_CompleteStream_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"message":"rate limit exceeded","type":"rate_limit"}}`)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	_, err := c.CompleteStream(context.Background(), core.Request{
		Model:    "m",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error for 403 response")
	}
	if !strings.Contains(err.Error(), "rate limit exceeded") {
		t.Errorf("error %q should contain API message", err.Error())
	}
}

func TestClient_Complete_ForwardsAllRequestFields(t *testing.T) {
	temp := 0.7
	maxTok := 100

	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"x","model":"m","choices":[],"usage":{}}`)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	_, _ = c.Complete(context.Background(), core.Request{
		Model:       "m",
		Messages:    []core.Message{{Role: "user", Content: "hi"}},
		Temperature: &temp,
		MaxTokens:   &maxTok,
	})

	var reqMap map[string]any
	if err := json.Unmarshal(capturedBody, &reqMap); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if reqMap["temperature"] != temp {
		t.Errorf("temperature = %v, want %v", reqMap["temperature"], temp)
	}
	if int(reqMap["max_tokens"].(float64)) != maxTok {
		t.Errorf("max_tokens = %v, want %d", reqMap["max_tokens"], maxTok)
	}
}
