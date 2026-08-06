package sse

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
)

// writtenChunks replays the SSE body a stream produced, decoded back into the
// chunks a client would see. The [DONE] sentinel is not JSON and is skipped.
func writtenChunks(t *testing.T, chunks []providers.StreamChunk) []providers.StreamChunk {
	t.Helper()

	ch := make(chan providers.StreamChunk, len(chunks))
	for _, c := range chunks {
		ch <- c
	}
	close(ch)

	w := httptest.NewRecorder()
	Write(context.Background(), w, ch, nil)

	var out []providers.StreamChunk
	for _, line := range strings.Split(w.Body.String(), "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok || data == "[DONE]" {
			continue
		}
		var got providers.StreamChunk
		if err := json.Unmarshal([]byte(data), &got); err != nil {
			t.Fatalf("decoding %q: %v", data, err)
		}
		out = append(out, got)
	}
	return out
}

// A provider that reports its message id once, on an opening event, leaves
// every later chunk carrying none. The client sees the whole stream, so the id
// is carried across it — this is the wiring test for the normalizer's contract,
// which providers/core covers in isolation.
func TestWrite_CarriesStreamIdentityAcrossChunks(t *testing.T) {
	got := writtenChunks(t, []providers.StreamChunk{
		{
			ID: "msg-1", Model: "command-r",
			Choices: []providers.StreamChoice{{Index: 0, Delta: providers.MessageDelta{Content: "a"}}},
		},
		{Choices: []providers.StreamChoice{{Index: 0, Delta: providers.MessageDelta{Content: "b"}}}},
		{Choices: []providers.StreamChoice{{Index: 0, FinishReason: "stop"}}},
	})

	if len(got) != 3 {
		t.Fatalf("wrote %d chunks, want 3", len(got))
	}
	for i, c := range got {
		if c.ID != "msg-1" {
			t.Errorf("chunk %d id = %q, want %q", i, c.ID, "msg-1")
		}
		if c.Model != "command-r" {
			t.Errorf("chunk %d model = %q, want %q", i, c.Model, "command-r")
		}
	}
}

// delta.role belongs on the first chunk and nowhere else, whatever the provider
// did. Both directions are covered: a provider that repeats it on every chunk,
// and one that never sends it at all.
func TestWrite_EmitsRoleOnFirstChunkOnly(t *testing.T) {
	tests := map[string][]string{
		"role repeated on every chunk": {"assistant", "assistant", "assistant"},
		"role never sent":              {"", "", ""},
	}

	for name, roles := range tests {
		t.Run(name, func(t *testing.T) {
			in := make([]providers.StreamChunk, 0, len(roles))
			for _, role := range roles {
				in = append(in, providers.StreamChunk{
					ID: "msg-1", Model: "m",
					Choices: []providers.StreamChoice{{
						Index: 0,
						Delta: providers.MessageDelta{Role: role, Content: "x"},
					}},
				})
			}

			got := writtenChunks(t, in)
			if len(got) != len(roles) {
				t.Fatalf("wrote %d chunks, want %d", len(got), len(roles))
			}
			if got[0].Choices[0].Delta.Role != "assistant" {
				t.Errorf("first chunk role = %q, want %q", got[0].Choices[0].Delta.Role, "assistant")
			}
			for i, c := range got[1:] {
				if role := c.Choices[0].Delta.Role; role != "" {
					t.Errorf("chunk %d role = %q, want empty", i+1, role)
				}
			}
		})
	}
}
