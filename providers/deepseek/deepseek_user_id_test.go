package deepseek

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// captureDeepSeekBody drives one surface against a stub and returns the raw
// JSON keys forwarded to DeepSeek.
func captureDeepSeekBody(t *testing.T, req core.Request, stream bool) map[string]json.RawMessage {
	t.Helper()
	var body map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"x","model":"deepseek-chat","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
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
		return body
	}
	if _, err := p.Complete(context.Background(), req); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	return body
}

// TestUserForwardedAsUserID pins the wire key DeepSeek's end-user identifier
// travels under. DeepSeek documents the field as "user_id"; the gateway's
// core.Request carries OpenAI's "user", and forwarding it verbatim discarded the
// caller's abuse-tracking intent under a key DeepSeek's schema does not define.
//
// Both surfaces are asserted because Complete builds its own body (it needs
// DeepSeek's extended usage decode) while CompleteStream goes through the shared
// builder — the exact place the two can drift apart.
func TestUserForwardedAsUserID(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "Complete"
		if stream {
			name = "CompleteStream"
		}

		t.Run(name+"/user is renamed to user_id", func(t *testing.T) {
			body := captureDeepSeekBody(t, core.Request{
				Model:    "deepseek-chat",
				Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
				User:     "tenant-42",
			}, stream)
			if got := string(body["user_id"]); got != `"tenant-42"` {
				t.Errorf("user_id = %s, want \"tenant-42\"", got)
			}
			if _, ok := body["user"]; ok {
				t.Errorf("\"user\" reached the wire; DeepSeek's schema defines user_id. body=%v", body)
			}
		})

		t.Run(name+"/no user sends neither key", func(t *testing.T) {
			body := captureDeepSeekBody(t, core.Request{
				Model:    "deepseek-chat",
				Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
			}, stream)
			if _, ok := body["user_id"]; ok {
				t.Errorf("user_id invented for a request that set no user. body=%v", body)
			}
			if _, ok := body["user"]; ok {
				t.Errorf("user present on a request that set none. body=%v", body)
			}
		})

		t.Run(name+"/other fields still travel", func(t *testing.T) {
			temp := 0.5
			body := captureDeepSeekBody(t, core.Request{
				Model:       "deepseek-chat",
				Messages:    []core.Message{{Role: core.RoleUser, Content: "hi"}},
				Temperature: &temp,
				User:        "tenant-42",
			}, stream)
			if got := string(body["temperature"]); got != "0.5" {
				t.Errorf("temperature = %s, want 0.5 — the body transform must forward every other field unchanged", got)
			}
			if got := string(body["model"]); got != `"deepseek-chat"` {
				t.Errorf("model = %s, want \"deepseek-chat\"", got)
			}
		})
	}
}
