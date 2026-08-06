package cohere

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// streamChunks runs one CompleteStream against a stub serving sseData.
func streamChunks(t *testing.T, sseData string) []core.StreamChunk {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseData))
	}))
	defer srv.Close()

	p, err := New("test-key", srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ch, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "command-r-plus",
		Messages: []core.Message{{Role: core.RoleUser, Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	var chunks []core.StreamChunk
	for c := range ch {
		chunks = append(chunks, c)
	}
	return chunks
}

// TestCompleteStream_MessageEndErrorFailsTheStream pins the mid-stream failure
// case. Cohere answered 200 and sent real content before failing, and the
// message-end event still arrives — so with the error unread the stream ended
// with a plain finish_reason and the gateway delivered a truncated answer as a
// success, metering, caching and billing it as one, and never firing retry or
// fallback.
func TestCompleteStream_MessageEndErrorFailsTheStream(t *testing.T) {
	chunks := streamChunks(t,
		"data: {\"type\":\"content-delta\",\"delta\":{\"message\":{\"content\":{\"text\":\"Hel\"}}}}\n\n"+
			"data: {\"type\":\"message-end\",\"delta\":{\"finish_reason\":\"ERROR\",\"error\":\"upstream model timeout\"}}\n\n")

	if len(chunks) == 0 {
		t.Fatal("no chunks")
	}
	last := chunks[len(chunks)-1]
	if last.Error == nil {
		t.Fatalf("terminal chunk carries no Error, so a failed stream is reported as a completion: %+v", last)
	}
	if !strings.Contains(last.Error.Error(), "upstream model timeout") {
		t.Errorf("Error = %q, want it to carry the upstream reason", last.Error)
	}
	if len(last.Choices) != 0 {
		t.Errorf("error chunk carries choices %+v; a failure is not a completion", last.Choices)
	}
	// The content that did arrive is still forwarded — the stream failed, it was
	// not empty.
	if chunks[0].Choices[0].Delta.Content != "Hel" {
		t.Errorf("first chunk content = %q, want Hel", chunks[0].Choices[0].Delta.Content)
	}
}

// TestCompleteStream_MessageEndWithoutErrorStillCompletes is the other half: a
// normal message-end must keep reporting a finish_reason and usage, so the guard
// above cannot be satisfied by failing every stream.
func TestCompleteStream_MessageEndWithoutErrorStillCompletes(t *testing.T) {
	chunks := streamChunks(t,
		"data: {\"type\":\"content-delta\",\"delta\":{\"message\":{\"content\":{\"text\":\"Hello\"}}}}\n\n"+
			"data: {\"type\":\"message-end\",\"delta\":{\"finish_reason\":\"COMPLETE\",\"usage\":{\"tokens\":{\"input_tokens\":5,\"output_tokens\":2}}}}\n\n")

	last := chunks[len(chunks)-1]
	if last.Error != nil {
		t.Fatalf("clean stream reported an error: %v", last.Error)
	}
	if last.Choices[0].FinishReason != core.FinishReasonStop {
		t.Errorf("finish_reason = %q, want %q", last.Choices[0].FinishReason, core.FinishReasonStop)
	}
	if last.Usage == nil || last.Usage.TotalTokens != 7 {
		t.Errorf("usage = %+v, want 7 total tokens", last.Usage)
	}
}
