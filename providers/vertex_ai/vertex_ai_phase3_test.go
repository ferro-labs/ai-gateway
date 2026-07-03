package vertexai

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

// vertexChatModel runs one Complete against a stub and returns the "model" field
// of the request body the provider sent.
func vertexChatModel(t *testing.T, reqModel string) string {
	t.Helper()
	var body struct {
		Model string `json:"model"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"x","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer srv.Close()

	p, err := New(Options{ProjectID: "demo-project", Region: "us-central1", APIKey: testAPIKey})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p.SetBaseURL(srv.URL)
	if _, err := p.Complete(context.Background(), core.Request{
		Model:    reqModel,
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	return body.Model
}

// TestComplete_PrefixesGooglePublisher verifies a first-party model is sent with
// the required "google/" publisher prefix, and an existing publisher prefix is
// left intact.
func TestComplete_PrefixesGooglePublisher(t *testing.T) {
	if got := vertexChatModel(t, "gemini-2.5-flash"); got != "google/gemini-2.5-flash" {
		t.Errorf("model = %q, want google/gemini-2.5-flash", got)
	}
	if got := vertexChatModel(t, "google/gemini-2.5-flash"); got != "google/gemini-2.5-flash" {
		t.Errorf("already-prefixed model = %q, want unchanged", got)
	}
	if got := vertexChatModel(t, "meta/llama-3.1-405b"); got != "meta/llama-3.1-405b" {
		t.Errorf("non-Google publisher model = %q, want unchanged", got)
	}
}

// TestComplete_ErrorPathReturnsAPIError verifies a non-2xx chat response surfaces
// via core.APIError.
func TestComplete_ErrorPathReturnsAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"quota exceeded"}}`)
	}))
	defer srv.Close()

	p, err := New(Options{ProjectID: "demo-project", Region: "us-central1", APIKey: testAPIKey})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p.SetBaseURL(srv.URL)
	_, err = p.Complete(context.Background(), core.Request{
		Model:    "gemini-2.5-flash",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error for 429")
	}
	if got := err.Error(); !strings.Contains(got, "quota exceeded") || !strings.Contains(got, "429") {
		t.Fatalf("error = %v, want status + upstream message", err)
	}
}

func TestBuildImagenRequest_MapsSizeToAspectRatio(t *testing.T) {
	cases := map[string]string{
		"1024x1024": "1:1",
		"1792x1024": "16:9",
		"1024x1792": "9:16",
		"640x480":   "",
	}
	for size, want := range cases {
		got := buildImagenRequest(core.ImageRequest{Prompt: "x", Model: "imagen-4.0-generate-001", Size: size})
		var ar string
		if got.Parameters != nil {
			ar = got.Parameters.AspectRatio
		}
		if ar != want {
			t.Errorf("size %q -> aspectRatio %q, want %q", size, ar, want)
		}
	}
}
