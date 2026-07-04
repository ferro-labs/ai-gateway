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

// TestPostStream_UpstreamError verifies the shared streaming path surfaces a
// non-200 upstream response as an error before starting any reader goroutine:
// the returned channel is nil, and the error carries both the recoverable status
// code (via core.ParseStatusCode) and the upstream message. This is the
// streaming counterpart to PostChat's non-200 branch and covers every provider
// that delegates to PostStream (cerebras, deepseek, fireworks, mistral, together).
func TestPostStream_UpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"rate limited"}}`)
	}))
	defer srv.Close()

	ch, err := PostStream(context.Background(), ChatParams{
		HTTPClient: srv.Client(),
		URL:        srv.URL,
		Headers:    map[string]string{"Content-Type": "application/json"},
		Provider:   "test",
		Label:      "test",
	}, core.Request{Model: "m", Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}}})

	if err == nil {
		t.Fatal("PostStream() error = nil, want upstream error")
	}
	if ch != nil {
		t.Errorf("PostStream() channel = %v, want nil on error (no reader goroutine started)", ch)
	}
	if got := core.ParseStatusCode(err); got != http.StatusTooManyRequests {
		t.Errorf("ParseStatusCode(err) = %d, want %d", got, http.StatusTooManyRequests)
	}
	if !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("error = %v, want it to contain upstream message %q", err, "rate limited")
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

// TestPostChat_CapturesExtraResponseFields verifies opt-in capture of
// provider-specific top-level response fields into core.Response.Metadata.
func TestPostChat_CapturesExtraResponseFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"x","model":"sonar","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"total_tokens":1},"citations":["https://a.example","https://b.example"]}`)
	}))
	defer srv.Close()

	resp, err := PostChat(context.Background(), ChatParams{
		HTTPClient:          srv.Client(),
		URL:                 srv.URL,
		Headers:             map[string]string{"Content-Type": "application/json"},
		Provider:            "test",
		Label:               "test",
		ExtraResponseFields: []string{"citations", "search_results"},
	}, core.Request{Model: "sonar", Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatalf("PostChat: %v", err)
	}
	cits, ok := resp.Metadata["citations"].([]any)
	if !ok || len(cits) != 2 {
		t.Fatalf("Metadata[citations] = %#v, want a 2-element slice", resp.Metadata["citations"])
	}
	if _, ok := resp.Metadata["search_results"]; ok {
		t.Error("search_results (absent upstream) must not appear in metadata")
	}
}

// TestPostEmbeddings_ForwardsInputType verifies the shared embeddings body now
// forwards the input_type field.
func TestPostEmbeddings_ForwardsInputType(t *testing.T) {
	var body map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[{"index":0,"embedding":[0.1]}],"model":"m","usage":{}}`)
	}))
	defer srv.Close()

	if _, err := PostEmbeddings(context.Background(), EmbeddingParams{
		HTTPClient: srv.Client(),
		URL:        srv.URL,
		Headers:    map[string]string{"Content-Type": "application/json"},
		Label:      "test",
	}, core.EmbeddingRequest{Model: "m", Input: "hello", InputType: "query"}); err != nil {
		t.Fatalf("PostEmbeddings: %v", err)
	}
	if got := string(body["input_type"]); got != `"query"` {
		t.Errorf("input_type = %s, want \"query\"", got)
	}
}
