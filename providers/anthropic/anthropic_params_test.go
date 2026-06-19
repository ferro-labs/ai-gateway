package anthropic

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

// TestComplete_MapsSupportedParams_DropsRest verifies #140 native wiring:
// top_p and stop map to the Anthropic field names, and params the Messages API
// cannot express are not forwarded.
func TestComplete_MapsSupportedParams_DropsRest(t *testing.T) {
	var captured map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"x","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}],"model":"claude","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer srv.Close()

	p, err := New("test-key", srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, _ = p.Complete(context.Background(), core.Request{
		Model:           "claude-3-5-sonnet",
		Messages:        []core.Message{{Role: "user", Content: "hi"}},
		TopP:            f64(0.9),
		Stop:            []string{"END"},
		PresencePenalty: f64(0.5), // unsupported → dropped
		Seed:            i64(42),  // unsupported → dropped
		LogitBias:       map[string]float64{"1": -1},
	})

	for _, k := range []string{"top_p", "stop_sequences"} {
		if _, ok := captured[k]; !ok {
			t.Errorf("expected %q forwarded; keys=%v", k, mapKeys(captured))
		}
	}
	for _, k := range []string{"presence_penalty", "frequency_penalty", "seed", "logit_bias"} {
		if _, ok := captured[k]; ok {
			t.Errorf("param %q should NOT be forwarded to Anthropic", k)
		}
	}
}

func mapKeys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
