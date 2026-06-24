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

func f64(f float64) *float64 { return &f }
func i64(i int64) *int64     { return &i }
func intp(i int) *int        { return &i }

// TestComplete_ForwardsSamplingParams is the #140 regression guard for the
// OpenAI-compatible builder path: params that used to be silently dropped must
// now reach the upstream request body.
func TestComplete_ForwardsSamplingParams(t *testing.T) {
	var captured map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"x","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{}}`)
	}))
	defer srv.Close()

	p, err := New("test-key", srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = p.Complete(context.Background(), core.Request{
		Model:            "llama-3.1-8b-instant",
		Messages:         []core.Message{{Role: "user", Content: "hi"}},
		Temperature:      f64(0.7),
		TopP:             f64(0.9),
		N:                intp(2),
		Seed:             i64(42),
		MaxTokens:        intp(64),
		PresencePenalty:  f64(0.5),
		FrequencyPenalty: f64(0.25),
		Stop:             []string{"END"},
		ResponseFormat:   &core.ResponseFormat{Type: "json_object"},
		User:             "u-1",
		LogitBias:        map[string]float64{"50256": -100},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	for _, k := range []string{
		"temperature", "top_p", "n", "seed", "max_tokens",
		"presence_penalty", "frequency_penalty", "stop",
		"response_format", "user", "logit_bias",
	} {
		if _, ok := captured[k]; !ok {
			t.Errorf("param %q not forwarded to upstream; captured keys=%v", k, keys(captured))
		}
	}
}

func keys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
