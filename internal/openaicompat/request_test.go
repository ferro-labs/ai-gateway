package openaicompat

import (
	"encoding/json"
	"io"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

func ptrF(f float64) *float64 { return &f }
func ptrI(i int) *int         { return &i }
func ptrI64(i int64) *int64   { return &i }

// TestBuildBody_ForwardsEverySamplingParam is the regression guard for #140:
// the shared builder must forward every OpenAI-shaped sampling/output field, not
// the old {model,messages,temperature,max_tokens} subset.
func TestBuildBody_ForwardsEverySamplingParam(t *testing.T) {
	req := core.Request{
		Model:               "some-model",
		Messages:            []core.Message{{Role: "user", Content: "hi"}},
		Temperature:         ptrF(0.7),
		TopP:                ptrF(0.9),
		N:                   ptrI(2),
		Seed:                ptrI64(42),
		MaxTokens:           ptrI(100),
		MaxCompletionTokens: ptrI(120),
		PresencePenalty:     ptrF(0.5),
		FrequencyPenalty:    ptrF(0.25),
		Stop:                []string{"END"},
		ResponseFormat:      &core.ResponseFormat{Type: "json_object"},
		TopLogProbs:         ptrI(3),
		LogProbs:            true,
		User:                "user-123",
		LogitBias:           map[string]float64{"50256": -100},
	}

	body, _, release, err := BuildBody(req, false)
	if err != nil {
		t.Fatalf("BuildBody returned error: %v", err)
	}
	defer release()

	raw, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	var got map[string]json.RawMessage
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal body: %v\nbody=%s", err, raw)
	}

	mustHave := []string{
		"model", "messages", "temperature", "top_p", "n", "seed",
		"max_tokens", "max_completion_tokens", "presence_penalty",
		"frequency_penalty", "stop", "response_format", "logprobs",
		"top_logprobs", "user", "logit_bias",
	}
	for _, k := range mustHave {
		if _, ok := got[k]; !ok {
			t.Errorf("field %q was dropped by BuildBody; body=%s", k, raw)
		}
	}
}

func TestBuildBody_StreamFlag(t *testing.T) {
	req := core.Request{Model: "m", Messages: []core.Message{{Role: "user", Content: "x"}}}

	// stream=false → omitempty drops the field (matches prior per-provider behaviour).
	body, _, release, err := BuildBody(req, false)
	if err != nil {
		t.Fatalf("BuildBody: %v", err)
	}
	raw, _ := io.ReadAll(body)
	release()
	var off map[string]json.RawMessage
	_ = json.Unmarshal(raw, &off)
	if _, ok := off["stream"]; ok {
		t.Errorf("stream should be omitted when false; body=%s", raw)
	}

	// stream=true → field present and true.
	body2, _, release2, err := BuildBody(req, true)
	if err != nil {
		t.Fatalf("BuildBody: %v", err)
	}
	raw2, _ := io.ReadAll(body2)
	release2()
	var on struct {
		Stream bool `json:"stream"`
	}
	if err := json.Unmarshal(raw2, &on); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !on.Stream {
		t.Errorf("stream should be true; body=%s", raw2)
	}
}
