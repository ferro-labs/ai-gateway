package deepseek

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// TestComplete_SurfacesReasoningAndCacheTokens verifies #145: deepseek-reasoner's
// reasoning_content and cache-hit token usage are surfaced rather than dropped.
func TestComplete_SurfacesReasoningAndCacheTokens(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"x","model":"deepseek-reasoner",
			"choices":[{"index":0,"message":{"role":"assistant","content":"42","reasoning_content":"first I think..."},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_cache_hit_tokens":8,"prompt_cache_miss_tokens":2,"completion_tokens_details":{"reasoning_tokens":3}}
		}`)
	}))
	defer srv.Close()

	p, err := New("test-key", srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	resp, err := p.Complete(context.Background(), core.Request{
		Model:    "deepseek-reasoner",
		Messages: []core.Message{{Role: core.RoleUser, Content: "q"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if got := resp.Choices[0].Message.ReasoningContent; got != "first I think..." {
		t.Errorf("reasoning_content = %q, want %q", got, "first I think...")
	}
	if resp.Usage.CacheReadTokens != 8 {
		t.Errorf("CacheReadTokens = %d, want 8", resp.Usage.CacheReadTokens)
	}
	if resp.Usage.ReasoningTokens != 3 {
		t.Errorf("ReasoningTokens = %d, want 3", resp.Usage.ReasoningTokens)
	}
	if resp.Usage.PromptTokens != 10 || resp.Usage.CompletionTokens != 5 || resp.Usage.TotalTokens != 15 {
		t.Errorf("base usage = %+v, want 10/5/15", resp.Usage)
	}
}

// TestCompleteStream_SurfacesReasoningDelta verifies reasoning_content is
// forwarded on streaming deltas.
func TestCompleteStream_SurfacesReasoningDelta(t *testing.T) {
	stream := "data: {\"id\":\"x\",\"model\":\"deepseek-reasoner\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"hmm\"}}]}\n\n" +
		"data: {\"id\":\"x\",\"model\":\"deepseek-reasoner\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"42\"}}]}\n\n" +
		"data: [DONE]\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, stream)
	}))
	defer srv.Close()

	p, err := New("test-key", srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ch, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "deepseek-reasoner",
		Messages: []core.Message{{Role: core.RoleUser, Content: "q"}},
	})
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}

	var reasoning, content strings.Builder
	for chunk := range ch {
		if chunk.Error != nil {
			t.Fatalf("stream error: %v", chunk.Error)
		}
		for _, c := range chunk.Choices {
			reasoning.WriteString(c.Delta.ReasoningContent)
			content.WriteString(c.Delta.Content)
		}
	}
	if reasoning.String() != "hmm" {
		t.Errorf("streamed reasoning = %q, want hmm", reasoning.String())
	}
	if content.String() != "42" {
		t.Errorf("streamed content = %q, want 42", content.String())
	}
}
