package together

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// captureTogetherBody drives one surface against a stub and returns the raw JSON
// keys forwarded to Together.
func captureTogetherBody(t *testing.T, req core.Request, stream bool) map[string]json.RawMessage {
	t.Helper()
	var body map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"x","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
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

// TestLogProbsSentAsTogetherCount pins the logprobs wire shape. Together
// consolidates OpenAI's boolean logprobs + integer top_logprobs into ONE integer
// logprobs field and defines no top_logprobs request parameter, so the OpenAI
// shape asked for log probabilities in a form Together does not accept.
func TestLogProbsSentAsTogetherCount(t *testing.T) {
	five := 5

	for _, stream := range []bool{false, true} {
		name := "Complete"
		if stream {
			name = "CompleteStream"
		}
		base := func() core.Request {
			return core.Request{
				Model:    "m",
				Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
			}
		}

		t.Run(name+"/top_logprobs becomes the logprobs count", func(t *testing.T) {
			req := base()
			req.LogProbs = true
			req.TopLogProbs = &five
			body := captureTogetherBody(t, req, stream)

			if got := string(body["logprobs"]); got != "5" {
				t.Errorf("logprobs = %s, want the integer 5 (Together types this field as a count)", got)
			}
			if _, ok := body["top_logprobs"]; ok {
				t.Errorf("top_logprobs reached the wire; Together defines no such request field. body=%v", body)
			}
		})

		t.Run(name+"/logprobs without a count asks for one", func(t *testing.T) {
			req := base()
			req.LogProbs = true
			body := captureTogetherBody(t, req, stream)

			if got := string(body["logprobs"]); got != "1" {
				t.Errorf("logprobs = %s, want 1 — the field must carry a number, and 1 is the smallest request that still returns what was asked for", got)
			}
		})

		t.Run(name+"/no logprobs requested sends neither field", func(t *testing.T) {
			req := base()
			req.TopLogProbs = &five // set without logprobs: OpenAI ignores it, so nothing is requested
			body := captureTogetherBody(t, req, stream)

			if _, ok := body["logprobs"]; ok {
				t.Errorf("logprobs sent for a request that did not ask for it. body=%v", body)
			}
			if _, ok := body["top_logprobs"]; ok {
				t.Errorf("top_logprobs reached the wire. body=%v", body)
			}
		})

		t.Run(name+"/other fields still travel", func(t *testing.T) {
			temp := 0.5
			req := base()
			req.Temperature = &temp
			req.User = "tenant-42"
			body := captureTogetherBody(t, req, stream)

			if got := string(body["temperature"]); got != "0.5" {
				t.Errorf("temperature = %s, want 0.5 — the body transform must forward every other field unchanged", got)
			}
			if got := string(body["user"]); got != `"tenant-42"` {
				t.Errorf("user = %s, want \"tenant-42\"", got)
			}
		})
	}
}
