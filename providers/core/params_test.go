package core

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func pF(f float64) *float64 { return &f }
func pI(i int) *int         { return &i }
func pI64(i int64) *int64   { return &i }

func TestDroppedParamsForProvider_MatrixDriven(t *testing.T) {
	// Gemini: logit_bias and user are Unsupported; response_format is Translate
	// (not dropped); temperature is Forward (not dropped).
	req := Request{
		Temperature:    pF(0.5),
		User:           "u",
		LogitBias:      map[string]float64{"1": 1},
		ResponseFormat: &ResponseFormat{Type: "json_object"},
	}
	got := DroppedParamsForProvider(req, "gemini")
	want := []string{"user", "logit_bias"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DroppedParamsForProvider(gemini) = %v, want %v", got, want)
	}
}

func TestDroppedParamsForProvider_UnknownProviderForwardsAll(t *testing.T) {
	req := Request{Seed: pI64(7), LogitBias: map[string]float64{"1": 1}}
	if got := DroppedParamsForProvider(req, "novita"); got != nil {
		t.Errorf("provider without a matrix entry drops nothing, got %v", got)
	}
}

func TestEnforceUnsupportedParams_WarnAndDropReturnNil(t *testing.T) {
	req := Request{LogitBias: map[string]float64{"1": 1}} // unsupported on gemini
	for _, mode := range []UnsupportedParamMode{UnsupportedParamWarn, UnsupportedParamDrop} {
		ctx := WithUnsupportedParamMode(context.Background(), mode)
		if err := EnforceUnsupportedParams(ctx, "gemini", "gemini-2.0", req); err != nil {
			t.Errorf("mode %v: expected nil, got %v", mode, err)
		}
	}
}

func TestEnforceUnsupportedParams_RejectReturns400(t *testing.T) {
	req := Request{LogitBias: map[string]float64{"1": 1}}
	ctx := WithUnsupportedParamMode(context.Background(), UnsupportedParamReject)
	err := EnforceUnsupportedParams(ctx, "gemini", "gemini-2.0", req)
	if err == nil {
		t.Fatal("reject mode with an unsupported param must return an error")
	}
	var upErr *UnsupportedParamError
	if !errors.As(err, &upErr) {
		t.Fatalf("error is not *UnsupportedParamError: %T", err)
	}
	if code := ParseStatusCode(err); code != 400 {
		t.Errorf("ParseStatusCode = %d, want 400", code)
	}
}

func TestEnforceUnsupportedParams_NoUnsupportedIsNilEvenOnReject(t *testing.T) {
	req := Request{Temperature: pF(0.5)} // forwarded by gemini
	ctx := WithUnsupportedParamMode(context.Background(), UnsupportedParamReject)
	if err := EnforceUnsupportedParams(ctx, "gemini", "gemini-2.0", req); err != nil {
		t.Errorf("no unsupported param set; reject must not fire, got %v", err)
	}
}

func TestEnforceUnsupportedParamsList_RejectHonorsExplicitList(t *testing.T) {
	req := Request{Seed: pI64(1)}
	ctx := WithUnsupportedParamMode(context.Background(), UnsupportedParamReject)
	// "seed" is not in the supported list, so reject fires.
	if err := EnforceUnsupportedParamsList(ctx, "bedrock", "m", req, "temperature", "top_p"); err == nil {
		t.Fatal("reject with an unsupported param in the explicit-list variant must return an error")
	}
	// "seed" now supported → no error.
	if err := EnforceUnsupportedParamsList(ctx, "bedrock", "m", req, "seed"); err != nil {
		t.Errorf("param in supported list must not reject, got %v", err)
	}
}

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
	if ParamPopulated(Request{}, "logprobs") {
		t.Error("logprobs should not be populated when false")
	}
	if !ParamPopulated(Request{LogProbs: true}, "logprobs") {
		t.Error("logprobs should be populated when true")
	}
}

func TestNormalizeCompletionTokenLimits_FillsMaxTokensFromFallback(t *testing.T) {
	maxCompletionTokens := 17
	req := Request{MaxCompletionTokens: &maxCompletionTokens}

	req.NormalizeCompletionTokenLimits()

	if req.MaxTokens == nil || *req.MaxTokens != maxCompletionTokens {
		t.Fatalf("MaxTokens = %v, want %d", req.MaxTokens, maxCompletionTokens)
	}
	if req.MaxCompletionTokens == nil || *req.MaxCompletionTokens != maxCompletionTokens {
		t.Fatalf("MaxCompletionTokens = %v, want preserved %d", req.MaxCompletionTokens, maxCompletionTokens)
	}
}

func TestNormalizeCompletionTokenLimits_PreservesExplicitMaxTokens(t *testing.T) {
	maxTokens := 23
	maxCompletionTokens := 17
	req := Request{
		MaxTokens:           &maxTokens,
		MaxCompletionTokens: &maxCompletionTokens,
	}

	req.NormalizeCompletionTokenLimits()

	if req.MaxTokens == nil || *req.MaxTokens != maxTokens {
		t.Fatalf("MaxTokens = %v, want explicit %d", req.MaxTokens, maxTokens)
	}
	if req.MaxCompletionTokens == nil || *req.MaxCompletionTokens != maxCompletionTokens {
		t.Fatalf("MaxCompletionTokens = %v, want preserved %d", req.MaxCompletionTokens, maxCompletionTokens)
	}
}
