package anthropic

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// TestComplete_MapsUserToMetadata verifies the OpenAI "user" field is forwarded
// as Anthropic's metadata.user_id rather than dropped.
func TestComplete_MapsUserToMetadata(t *testing.T) {
	body := captureBody(t, core.Request{
		Model:    "claude-sonnet-5",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
		User:     "user_123",
	})

	raw, ok := body["metadata"]
	if !ok {
		t.Fatal("metadata not forwarded")
	}
	var md struct {
		UserID string `json:"user_id"`
	}
	if err := json.Unmarshal(raw, &md); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	if md.UserID != "user_123" {
		t.Errorf("metadata.user_id = %q, want user_123", md.UserID)
	}
}

type wireImageBlock struct {
	Type   string `json:"type"`
	Source struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
		URL       string `json:"url"`
	} `json:"source"`
}

// TestComplete_ReencodesNonBase64DataURI verifies a non-base64 data URI is
// re-encoded to a base64 image source instead of being emitted as an invalid
// url source.
func TestComplete_ReencodesNonBase64DataURI(t *testing.T) {
	body := captureBody(t, core.Request{
		Model: "claude-sonnet-5",
		Messages: []core.Message{{
			Role: core.RoleUser,
			ContentParts: []core.ContentPart{
				{Type: "image_url", ImageURL: &core.ImageURLPart{URL: "data:image/png,hello%20world"}},
			},
		}},
	})

	msgs := decodeMessages(t, body)
	var blocks []wireImageBlock
	if err := json.Unmarshal(msgs[0]["content"], &blocks); err != nil {
		t.Fatalf("decode content blocks: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("content blocks = %d, want 1: %+v", len(blocks), blocks)
	}
	src := blocks[0].Source
	if src.Type != "base64" {
		t.Fatalf("source type = %q, want base64 (non-base64 data URI must be re-encoded)", src.Type)
	}
	if src.MediaType != "image/png" {
		t.Errorf("media_type = %q, want image/png", src.MediaType)
	}
	// base64("hello world") == "aGVsbG8gd29ybGQ="
	if src.Data != "aGVsbG8gd29ybGQ=" {
		t.Errorf("data = %q, want base64 of the decoded payload", src.Data)
	}
}

// TestComplete_ErrorPathReturnsAPIError verifies a non-2xx chat response surfaces
// the upstream status and message via core.APIError.
func TestComplete_ErrorPathReturnsAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"overloaded"}}`))
	}))
	defer srv.Close()

	p, err := New("sk-test-key", srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = p.Complete(context.Background(), core.Request{
		Model:    "claude-sonnet-5",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error for 429")
	}
	if !strings.Contains(err.Error(), "overloaded") || !strings.Contains(err.Error(), "429") {
		t.Fatalf("error = %v, want status + upstream message", err)
	}
}
