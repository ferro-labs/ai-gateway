package cohere

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

// TestComplete_ForwardsVisionContent verifies multimodal image parts are mapped
// to Cohere content blocks instead of being dropped.
func TestComplete_ForwardsVisionContent(t *testing.T) {
	var body map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"x","message":{"role":"assistant","content":[{"type":"text","text":"ok"}]},"finish_reason":"COMPLETE","usage":{"tokens":{"input_tokens":1,"output_tokens":1}}}`)
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	if _, err := p.Complete(context.Background(), core.Request{
		Model: "command-r-plus",
		Messages: []core.Message{{
			Role: core.RoleUser,
			ContentParts: []core.ContentPart{
				{Type: "text", Text: "what is this"},
				{Type: "image_url", ImageURL: &core.ImageURLPart{URL: "https://example.com/cat.png"}},
			},
		}},
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	var msgs []struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(body["messages"], &msgs); err != nil {
		t.Fatalf("decode messages: %v", err)
	}
	if len(msgs) == 0 || !strings.Contains(string(msgs[0].Content), "image_url") ||
		!strings.Contains(string(msgs[0].Content), "example.com/cat.png") {
		t.Errorf("vision image not forwarded; content=%s", msgs[0].Content)
	}
}

// TestCompleteStream_SurfacesUsage verifies the message-end token usage is
// surfaced on a StreamChunk (previously parsed and discarded).
func TestCompleteStream_SurfacesUsage(t *testing.T) {
	sse := "data: {\"type\":\"content-delta\",\"delta\":{\"message\":{\"content\":{\"text\":\"hi\"}}}}\n\n" +
		"data: {\"type\":\"message-end\",\"delta\":{\"finish_reason\":\"COMPLETE\",\"usage\":{\"tokens\":{\"input_tokens\":7,\"output_tokens\":3}}}}\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse)
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	ch, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "command-r-plus",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	var usage *core.Usage
	for c := range ch {
		if c.Error != nil {
			t.Fatalf("stream error: %v", c.Error)
		}
		if c.Usage != nil {
			usage = c.Usage
		}
	}
	if usage == nil {
		t.Fatal("no usage surfaced on cohere stream")
	}
	if usage.PromptTokens != 7 || usage.CompletionTokens != 3 || usage.TotalTokens != 10 {
		t.Fatalf("usage = %+v, want 7/3/10", usage)
	}
}

func TestNew_RejectsInvalidBaseURL(t *testing.T) {
	if _, err := New("k", "://bad"); err == nil {
		t.Fatal("New accepted an invalid base URL")
	}
}
