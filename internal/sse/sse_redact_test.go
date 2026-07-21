package sse

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
)

// fakeUpstreamKey has the legacy OpenAI sk- key shape. It is not a real credential.
var fakeUpstreamKey = buildFakeKey("sk-", "abc123DEF456ghi789JKL012mno345")

// buildFakeKey joins prefix and body at runtime so no credential-shaped
// literal is committed for a scanner to flag. Mirrors the helper used by
// the redaction policy tests in internal/redact.
func buildFakeKey(prefix, body string) string { return prefix + body }

// A mid-stream failure carries provider-controlled text straight into the SSE
// data frame, so it is filtered before it reaches the client.
func TestWrite_RedactsErrorChunkMessage(t *testing.T) {
	ch := make(chan providers.StreamChunk, 1)
	ch <- providers.StreamChunk{
		Error: errors.New("openai API error (401): Incorrect API key provided: " + fakeUpstreamKey),
	}
	close(ch)

	w := httptest.NewRecorder()
	Write(context.Background(), w, ch)

	body := w.Body.String()
	if strings.Contains(body, fakeUpstreamKey) {
		t.Fatalf("SSE frame leaked the upstream key: %s", body)
	}
	if !strings.Contains(body, "[REDACTED_OPENAI_KEY]") {
		t.Errorf("expected a redaction token in the frame, got: %s", body)
	}
	// The frame shape clients parse must be unchanged.
	if !strings.Contains(body, `"type":"stream_error"`) || !strings.Contains(body, `"code":"stream_error"`) {
		t.Errorf("error frame lost its type/code: %s", body)
	}
}

// An error with nothing sensitive in it reaches the client verbatim.
func TestWrite_LeavesCleanErrorChunkIntact(t *testing.T) {
	ch := make(chan providers.StreamChunk, 1)
	ch <- providers.StreamChunk{Error: errors.New("upstream is unavailable")}
	close(ch)

	w := httptest.NewRecorder()
	Write(context.Background(), w, ch)

	if !strings.Contains(w.Body.String(), "upstream is unavailable") {
		t.Errorf("clean error message did not survive: %s", w.Body.String())
	}
}
