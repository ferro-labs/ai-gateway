package cohere

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

// TestComplete_MapsSupportedParams_DropsRest verifies #140 native wiring for
// Cohere v2: top_p maps to "p", stop to "stop_sequences", and seed/penalties
// are forwarded while unsupported params are not.
func TestComplete_MapsSupportedParams_DropsRest(t *testing.T) {
	var captured map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"x","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]},"finish_reason":"COMPLETE","usage":{"tokens":{"input_tokens":1,"output_tokens":1}}}`)
	}))
	defer srv.Close()

	p, err := New("test-key", srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, _ = p.Complete(context.Background(), core.Request{
		Model:            "command-r",
		Messages:         []core.Message{{Role: "user", Content: "hi"}},
		TopP:             f64(0.9),
		Seed:             i64(42),
		PresencePenalty:  f64(0.5),
		FrequencyPenalty: f64(0.25),
		Stop:             []string{"END"},
		N:                nil,
		LogitBias:        map[string]float64{"1": -1}, // unsupported → dropped
	})

	for _, k := range []string{"p", "stop_sequences", "seed", "presence_penalty", "frequency_penalty"} {
		if _, ok := captured[k]; !ok {
			t.Errorf("expected %q forwarded; keys=%v", k, mapKeys(captured))
		}
	}
	// Cohere uses "p", not "top_p"; logit_bias is unsupported.
	for _, k := range []string{"top_p", "logit_bias"} {
		if _, ok := captured[k]; ok {
			t.Errorf("param %q should NOT be forwarded to Cohere", k)
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
