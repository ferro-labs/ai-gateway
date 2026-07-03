package openaicompat

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

// TestPostStream_SetsIncludeUsage verifies the shared streaming path requests a
// terminal usage chunk (stream_options.include_usage) so every OpenAI-compatible
// provider reports streaming usage.
func TestPostStream_SetsIncludeUsage(t *testing.T) {
	var body map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	ch, err := PostStream(context.Background(), ChatParams{
		HTTPClient: srv.Client(),
		URL:        srv.URL,
		Headers:    map[string]string{"Content-Type": "application/json"},
		Provider:   "test",
		Label:      "test",
	}, core.Request{Model: "m", Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatalf("PostStream: %v", err)
	}
	var n int
	for range ch {
		n++
	}
	if n != 0 {
		t.Errorf("expected 0 chunks for a [DONE]-only stream, got %d", n)
	}

	so, ok := body["stream_options"]
	if !ok {
		t.Fatal("stream_options not set on streaming request")
	}
	if !strings.Contains(string(so), "include_usage") {
		t.Errorf("stream_options = %s, want include_usage", so)
	}
}

// TestPostChat_NormalizesFinishReason verifies a provider-specific finish reason
// (Mistral's model_length) is normalized to the canonical OpenAI value.
func TestPostChat_NormalizesFinishReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"x","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"model_length"}],"usage":{"total_tokens":1}}`)
	}))
	defer srv.Close()

	resp, err := PostChat(context.Background(), ChatParams{
		HTTPClient: srv.Client(),
		URL:        srv.URL,
		Headers:    map[string]string{"Content-Type": "application/json"},
		Provider:   "test",
		Label:      "test",
	}, core.Request{Model: "m", Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatalf("PostChat: %v", err)
	}
	if resp.Choices[0].FinishReason != core.FinishReasonLength {
		t.Errorf("finish_reason = %q, want length (normalized from model_length)", resp.Choices[0].FinishReason)
	}
}

// TestDecodeStreamChunk_NormalizesFinishReason verifies the shared stream decoder
// normalizes a provider-specific finish reason directly (not only through a
// provider round-trip).
func TestDecodeStreamChunk_NormalizesFinishReason(t *testing.T) {
	chunk, err := DecodeStreamChunk([]byte(`{"choices":[{"index":0,"delta":{},"finish_reason":"model_length"}]}`))
	if err != nil {
		t.Fatalf("DecodeStreamChunk: %v", err)
	}
	if chunk.Choices[0].FinishReason != core.FinishReasonLength {
		t.Errorf("finish_reason = %q, want length", chunk.Choices[0].FinishReason)
	}
}
