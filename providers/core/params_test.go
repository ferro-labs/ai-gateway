package core

import (
	"reflect"
	"testing"
)

func pF(f float64) *float64 { return &f }
func pI(i int) *int         { return &i }
func pI64(i int64) *int64   { return &i }

func TestDroppedParams_ReportsPopulatedUnsupportedInStableOrder(t *testing.T) {
	req := Request{
		Model:            "m",
		Temperature:      pF(0.5), // supported below
		TopP:             pF(0.9), // unsupported
		Seed:             pI64(1), // unsupported
		PresencePenalty:  pF(0.1), // unsupported
		FrequencyPenalty: pF(0.2), // unsupported
		Stop:             []string{"x"},
		LogitBias:        map[string]float64{"1": -1},
	}

	got := DroppedParams(req, "temperature", "stop")
	want := []string{"top_p", "seed", "presence_penalty", "frequency_penalty", "logit_bias"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DroppedParams = %v, want %v", got, want)
	}
}

func TestDroppedParams_NothingPopulated(t *testing.T) {
	req := Request{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}}
	if got := DroppedParams(req, "temperature"); got != nil {
		t.Errorf("expected no dropped params, got %v", got)
	}
}

func TestDroppedParams_AllSupported(t *testing.T) {
	req := Request{Temperature: pF(0.5), TopP: pF(0.9), MaxTokens: pI(10)}
	if got := DroppedParams(req, "temperature", "top_p", "max_tokens"); got != nil {
		t.Errorf("expected nothing dropped when all supported, got %v", got)
	}
}

func TestParamPopulated_BooleanLogprobs(t *testing.T) {
	if paramPopulated(Request{}, "logprobs") {
		t.Error("logprobs should not be populated when false")
	}
	if !paramPopulated(Request{LogProbs: true}, "logprobs") {
		t.Error("logprobs should be populated when true")
	}
}
