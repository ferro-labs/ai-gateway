package anthropic

import (
	"context"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// TestCompleteStream_PropagatesErrorEvent verifies that a mid-stream
// "event: error" frame (sent by the Anthropic Messages API when something
// goes wrong after streaming has started) is surfaced on the returned
// channel via StreamChunk.Error instead of being silently dropped, and that
// no further events are processed after it.
func TestCompleteStream_PropagatesErrorEvent(t *testing.T) {
	stream := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_1","model":"claude","role":"assistant","usage":{"input_tokens":10}}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}` + "\n\n" +
		"event: error\n" +
		`data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"should not appear"}}` + "\n\n"

	p := newTestProvider(t, stream)
	ch, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "claude-3-5-sonnet",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}

	var chunks []core.StreamChunk
	for chunk := range ch {
		chunks = append(chunks, chunk)
	}

	var sawError bool
	for i, chunk := range chunks {
		if chunk.Error == nil {
			continue
		}
		sawError = true
		if !strings.Contains(chunk.Error.Error(), "Overloaded") {
			t.Errorf("error chunk = %v, want message containing %q", chunk.Error, "Overloaded")
		}
		if !strings.Contains(chunk.Error.Error(), "overloaded_error") {
			t.Errorf("error chunk = %v, want type containing %q", chunk.Error, "overloaded_error")
		}
		if i != len(chunks)-1 {
			t.Errorf("error chunk at index %d, want last (stream must stop after error event)", i)
		}
	}
	if !sawError {
		t.Fatal("no error chunk surfaced for mid-stream error event")
	}

	for _, chunk := range chunks {
		for _, choice := range chunk.Choices {
			if strings.Contains(choice.Delta.Content, "should not appear") {
				t.Fatal("stream continued processing events after the error frame")
			}
		}
	}
}
