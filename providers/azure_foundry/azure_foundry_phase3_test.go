package azurefoundry

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// TestComplete_SetsExtraParametersAndDecodes verifies the request carries the
// "extra-parameters: drop" header and the response is decoded (content + usage).
func TestComplete_SetsExtraParametersAndDecodes(t *testing.T) {
	var extraParams string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		extraParams = r.Header.Get("extra-parameters")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`)
	}))
	defer srv.Close()

	p, err := New(testAPIKey, srv.URL, "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	resp, err := p.Complete(context.Background(), core.Request{
		Model:    "gpt-4o",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if extraParams != "drop" {
		t.Errorf("extra-parameters header = %q, want drop", extraParams)
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Message.Content != "hello" {
		t.Errorf("decoded choices = %+v", resp.Choices)
	}
	if resp.Usage.TotalTokens != 7 {
		t.Errorf("usage = %+v, want total 7", resp.Usage)
	}
}

func TestComplete_ErrorPathReturnsAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"bad model"}}`)
	}))
	defer srv.Close()

	p, err := New(testAPIKey, srv.URL, "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = p.Complete(context.Background(), core.Request{
		Model:    "gpt-4o",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error for 400")
	}
	if !strings.Contains(err.Error(), "bad model") || !strings.Contains(err.Error(), "400") {
		t.Fatalf("error = %v, want status + message", err)
	}
}

func TestNew_RejectsInvalidBaseURL(t *testing.T) {
	if _, err := New(testAPIKey, "://bad", ""); err == nil {
		t.Fatal("New accepted an invalid base URL")
	}
}
