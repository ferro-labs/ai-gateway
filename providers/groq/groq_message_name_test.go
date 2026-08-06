package groq

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// captureGroqMessages drives one surface against a stub and returns the messages
// array exactly as it reached Groq.
func captureGroqMessages(t *testing.T, req core.Request, stream bool) []map[string]json.RawMessage {
	t.Helper()
	var body struct {
		Messages []map[string]json.RawMessage `json:"messages"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"x","model":"llama-3.3-70b-versatile","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer srv.Close()

	p, err := New("test-key", srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if stream {
		ch, err := p.CompleteStream(context.Background(), req)
		if err != nil {
			t.Fatalf("CompleteStream: %v", err)
		}
		for range ch { //nolint:revive // drain so the producer finishes before the stub closes
		}
		return body.Messages
	}
	if _, err := p.Complete(context.Background(), req); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	return body.Messages
}

// TestMessageNameIsStripped pins that messages[].name never reaches Groq. Groq
// documents it among the fields that "will result in a 400 error if supplied",
// so a plain OpenAI request that names its participants failed here and nowhere
// else in the fleet.
func TestMessageNameIsStripped(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "Complete"
		if stream {
			name = "CompleteStream"
		}

		t.Run(name+"/name is removed, content survives", func(t *testing.T) {
			msgs := captureGroqMessages(t, core.Request{
				Model: "llama-3.3-70b-versatile",
				Messages: []core.Message{
					{Role: core.RoleUser, Content: "hi", Name: "alice"},
					{Role: core.RoleAssistant, Content: "hello", Name: "bot"},
				},
			}, stream)
			if len(msgs) != 2 {
				t.Fatalf("got %d messages, want 2", len(msgs))
			}
			for i, m := range msgs {
				if _, ok := m["name"]; ok {
					t.Errorf("messages[%d] carries \"name\"; Groq answers 400 for it", i)
				}
			}
			if got := string(msgs[0]["content"]); got != `"hi"` {
				t.Errorf("messages[0].content = %s, want \"hi\"", got)
			}
			if got := string(msgs[1]["role"]); got != `"assistant"` {
				t.Errorf("messages[1].role = %s, want \"assistant\"", got)
			}
		})
	}
}

// TestStripMessageNamesDoesNotMutateCaller guards the copy. The request is
// passed by value but its messages slice is not: clearing Name in place would
// reach through to the caller's own request, which the gateway reuses for
// retries, plugin stages and request logging — so a Groq attempt in a fallback
// chain would silently strip the field from the request every later target sees.
func TestStripMessageNamesDoesNotMutateCaller(t *testing.T) {
	original := core.Request{
		Model:    "llama-3.3-70b-versatile",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi", Name: "alice"}},
	}

	stripped := stripMessageNames(original)

	if stripped.Messages[0].Name != "" {
		t.Errorf("stripped name = %q, want empty", stripped.Messages[0].Name)
	}
	if original.Messages[0].Name != "alice" {
		t.Errorf("caller's message was mutated: name = %q, want alice", original.Messages[0].Name)
	}
}
