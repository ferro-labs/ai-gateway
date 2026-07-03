package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	core "github.com/ferro-labs/ai-gateway/providers/core"
)

// captureChatBody runs one chat request (streaming or not) against a stub and
// returns the decoded request body the provider sent.
func captureChatBody(t *testing.T, stream bool, req core.Request) map[string]json.RawMessage {
	t.Helper()
	var captured map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &captured)
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-1\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer srv.Close()

	p, err := New("sk-test", srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if stream {
		ch, err := p.CompleteStream(context.Background(), req)
		if err != nil {
			t.Fatalf("CompleteStream: %v", err)
		}
		var got int
		for range ch {
			got++
		}
		if got == 0 {
			t.Fatal("stream produced no chunks")
		}
		return captured
	}
	if _, err := p.Complete(context.Background(), req); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	return captured
}

// TestOpenAIProvider_ForwardsFieldsOnBothPaths is the #265 acceptance: a
// streaming request forwards the same logit_bias and multimodal image content as
// an identical non-streaming request, and streaming sets stream_options.
func TestOpenAIProvider_ForwardsFieldsOnBothPaths(t *testing.T) {
	req := core.Request{
		Model: "gpt-4o",
		Messages: []core.Message{{
			Role: core.RoleUser,
			ContentParts: []core.ContentPart{
				{Type: "text", Text: "what is this"},
				{Type: "image_url", ImageURL: &core.ImageURLPart{URL: "https://example.com/cat.png"}},
			},
		}},
		LogitBias: map[string]float64{"123": -100},
	}

	for _, stream := range []bool{false, true} {
		name := "non_stream"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			body := captureChatBody(t, stream, req)

			if _, ok := body["logit_bias"]; !ok {
				t.Error("logit_bias not forwarded")
			}

			var msgs []struct {
				Content json.RawMessage `json:"content"`
			}
			if err := json.Unmarshal(body["messages"], &msgs); err != nil {
				t.Fatalf("decode messages: %v", err)
			}
			if len(msgs) == 0 || !strings.Contains(string(msgs[0].Content), "image_url") {
				t.Errorf("vision image_url not forwarded; content = %s", msgs[0].Content)
			}

			if stream {
				if _, ok := body["stream_options"]; !ok {
					t.Error("stream_options not set on streaming request")
				}
			}
		})
	}
}

// TestOpenAIProvider_CompleteStream_ReportsUsageWithDetails verifies the raw SSE
// decoder preserves the nested reasoning/cache token detail on the final chunk.
func TestOpenAIProvider_CompleteStream_ReportsUsageWithDetails(t *testing.T) {
	sse := "data: {\"id\":\"chatcmpl-1\",\"model\":\"o1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"o1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":30,\"completion_tokens\":20,\"total_tokens\":50,\"prompt_tokens_details\":{\"cached_tokens\":8},\"completion_tokens_details\":{\"reasoning_tokens\":12}}}\n\n" +
		"data: [DONE]\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse)
	}))
	defer srv.Close()

	p, _ := New("sk-test", srv.URL)
	ch, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "o1",
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
		t.Fatal("no usage reported on stream")
	}
	if usage.PromptTokens != 30 || usage.CompletionTokens != 20 || usage.TotalTokens != 50 {
		t.Fatalf("usage tokens = %d/%d/%d, want 30/20/50",
			usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens)
	}
	if usage.ReasoningTokens != 12 {
		t.Errorf("reasoning tokens = %d, want 12", usage.ReasoningTokens)
	}
	if usage.CacheReadTokens != 8 {
		t.Errorf("cache read tokens = %d, want 8", usage.CacheReadTokens)
	}
}

func captureImageBody(t *testing.T, req core.ImageRequest, respBody string) map[string]json.RawMessage {
	t.Helper()
	var captured map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, respBody)
	}))
	defer srv.Close()

	p, _ := New("sk-test", srv.URL)
	if _, err := p.GenerateImage(context.Background(), req); err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}
	return captured
}

// TestOpenAIProvider_GenerateImage_GptImageOmitsResponseFormat verifies the
// response_format parameter is not sent for gpt-image-* models (which reject it).
func TestOpenAIProvider_GenerateImage_GptImageOmitsResponseFormat(t *testing.T) {
	body := captureImageBody(t,
		core.ImageRequest{Model: "gpt-image-1", Prompt: "a cat", ResponseFormat: "url"},
		`{"created":1,"data":[{"b64_json":"QUJD"}]}`)
	if raw, ok := body["response_format"]; ok {
		t.Errorf("response_format must be omitted for gpt-image-*, got %s", raw)
	}
}

// TestOpenAIProvider_GenerateImage_DallESetsResponseFormat verifies DALL·E models
// still receive response_format (defaulting to url).
func TestOpenAIProvider_GenerateImage_DallESetsResponseFormat(t *testing.T) {
	body := captureImageBody(t,
		core.ImageRequest{Model: "dall-e-3", Prompt: "a cat"},
		`{"created":1,"data":[{"url":"https://img"}]}`)
	raw, ok := body["response_format"]
	if !ok {
		t.Fatal("response_format must be set for dall-e-3")
	}
	var rf string
	if err := json.Unmarshal(raw, &rf); err != nil {
		t.Fatalf("decode response_format: %v", err)
	}
	if rf != "url" {
		t.Errorf("response_format = %q, want url (default)", rf)
	}
}
