package gemini

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

// TestComplete_MapsSupportedParamsToGenerationConfig verifies #140 native wiring
// for Gemini: OpenAI sampling params land under generationConfig with Gemini
// field names (topP, candidateCount, seed, stopSequences, penalties), and
// response_format JSON mode maps to responseMimeType.
func TestComplete_MapsSupportedParamsToGenerationConfig(t *testing.T) {
	var captured struct {
		GenerationConfig map[string]json.RawMessage `json:"generationConfig"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":"hi"}],"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`)
	}))
	defer srv.Close()

	p, err := New("test-key", srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, _ = p.Complete(context.Background(), core.Request{
		Model:            "gemini-1.5-pro",
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
		LogitBias:        map[string]float64{"1": -1}, // unsupported → dropped
	})

	gc := captured.GenerationConfig
	if gc == nil {
		t.Fatalf("generationConfig missing from request body")
	}
	for _, k := range []string{
		"temperature", "topP", "candidateCount", "seed", "maxOutputTokens",
		"presencePenalty", "frequencyPenalty", "stopSequences", "responseMimeType",
	} {
		if _, ok := gc[k]; !ok {
			t.Errorf("expected generationConfig.%s to be set", k)
		}
	}
}
