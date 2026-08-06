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

// TestNormalizeCompletionTokenLimits_ReconcilesBothFields pins the property the
// rest of the request path depends on: after normalization the two
// completion-length fields carry one value, so no consumer can be handed a
// different ceiling than the one a guardrail approved. The value is the one
// EffectiveMaxTokens reports — max_completion_tokens supersedes max_tokens,
// matching the API this request is written against.
func TestNormalizeCompletionTokenLimits_ReconcilesBothFields(t *testing.T) {
	tests := []struct {
		name                string
		maxTokens           *int
		maxCompletionTokens *int
		want                *int
	}{
		{name: "neither set", want: nil},
		{name: "only max_tokens", maxTokens: pI(23), want: pI(23)},
		{name: "only max_completion_tokens", maxCompletionTokens: pI(17), want: pI(17)},
		{name: "equal", maxTokens: pI(17), maxCompletionTokens: pI(17), want: pI(17)},
		{name: "both set: max_completion_tokens supersedes", maxTokens: pI(5), maxCompletionTokens: pI(500000), want: pI(500000)},
		{name: "both set, roles swapped", maxTokens: pI(500000), maxCompletionTokens: pI(5), want: pI(5)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := Request{MaxTokens: tt.maxTokens, MaxCompletionTokens: tt.maxCompletionTokens}

			req.NormalizeCompletionTokenLimits()

			if tt.want == nil {
				if req.MaxTokens != nil || req.MaxCompletionTokens != nil {
					t.Fatalf("MaxTokens = %v, MaxCompletionTokens = %v, want both nil", req.MaxTokens, req.MaxCompletionTokens)
				}
				return
			}
			if req.MaxTokens == nil || *req.MaxTokens != *tt.want {
				t.Errorf("MaxTokens = %v, want %d", req.MaxTokens, *tt.want)
			}
			// Only max_tokens set is the one case that leaves
			// max_completion_tokens absent: filling it would put a field on the
			// wire the caller never sent.
			if tt.maxCompletionTokens == nil {
				if req.MaxCompletionTokens != nil {
					t.Errorf("MaxCompletionTokens = %v, want nil when the caller did not send it", req.MaxCompletionTokens)
				}
				return
			}
			if req.MaxCompletionTokens == nil || *req.MaxCompletionTokens != *tt.want {
				t.Errorf("MaxCompletionTokens = %v, want %d", req.MaxCompletionTokens, *tt.want)
			}
			if req.MaxTokens == req.MaxCompletionTokens {
				t.Error("the two fields share one pointer; clamping one in place would silently move the other")
			}
		})
	}
}

// TestDroppedParamsForProvider_ReconciledMaxCompletionTokensNotDropped covers
// #141: a caller supplying only max_completion_tokens has it copied into
// MaxTokens by NormalizeCompletionTokenLimits() before enforcement runs. Every
// provider below reads req.MaxTokens natively, so the request is fully
// satisfiable even though max_completion_tokens itself is Unsupported in the
// matrix; it must not be reported as dropped, and reject mode must not fire.
func TestDroppedParamsForProvider_ReconciledMaxCompletionTokensNotDropped(t *testing.T) {
	req := Request{MaxCompletionTokens: pI(256)}
	req.NormalizeCompletionTokenLimits()

	providers := []string{"anthropic", "bedrock", "cohere", "gemini", "replicate"}
	ctx := WithUnsupportedParamMode(context.Background(), UnsupportedParamReject)
	for _, provider := range providers {
		if got := DroppedParamsForProvider(req, provider); got != nil {
			t.Errorf("provider %s: DroppedParamsForProvider = %v, want none", provider, got)
		}
		if err := EnforceUnsupportedParams(ctx, provider, "m", req); err != nil {
			t.Errorf("provider %s: reject mode returned %v, want nil", provider, err)
		}
	}
}

// TestDroppedParamsForProvider_MaxCompletionTokensReconciliationScoping proves
// the reconciliation guard is narrow: it only suppresses max_completion_tokens
// when its value has actually migrated into MaxTokens, and it never masks a
// genuinely unsupported parameter elsewhere on the same request.
func TestDroppedParamsForProvider_MaxCompletionTokensReconciliationScoping(t *testing.T) {
	tests := []struct {
		name        string
		req         Request
		wantDropped []string
	}{
		{
			name:        "not reconciled (NormalizeCompletionTokenLimits not called): still dropped",
			req:         Request{MaxCompletionTokens: pI(256)},
			wantDropped: []string{"max_completion_tokens"},
		},
		{
			// Normalization resolves two differing values to one, so after it
			// runs there is no distinct max_completion_tokens value left for
			// anthropic to be unable to honor: max_tokens carries it.
			name: "max_tokens and max_completion_tokens differ, then reconciled: not dropped",
			req: func() Request {
				r := Request{MaxTokens: pI(100), MaxCompletionTokens: pI(256)}
				r.NormalizeCompletionTokenLimits()
				return r
			}(),
			wantDropped: nil,
		},
		{
			// The same pair never normalized — reachable only from a
			// programmatically built Request — still reports the parameter the
			// provider cannot express.
			name:        "max_tokens and max_completion_tokens differ, not reconciled: still dropped",
			req:         Request{MaxTokens: pI(100), MaxCompletionTokens: pI(256)},
			wantDropped: []string{"max_completion_tokens"},
		},
		{
			name: "explicit max_tokens equals max_completion_tokens: not dropped",
			req: func() Request {
				r := Request{MaxTokens: pI(256), MaxCompletionTokens: pI(256)}
				r.NormalizeCompletionTokenLimits()
				return r
			}(),
			wantDropped: nil,
		},
		{
			name: "reconciled max_completion_tokens alongside a genuinely unsupported param",
			req: func() Request {
				r := Request{MaxCompletionTokens: pI(256), PresencePenalty: pF(0.1)}
				r.NormalizeCompletionTokenLimits()
				return r
			}(),
			// Only presence_penalty should surface: the reconciliation guard must
			// not blanket-disable enforcement for the rest of the request.
			wantDropped: []string{"presence_penalty"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DroppedParamsForProvider(tt.req, "anthropic")
			if len(got) != 0 || len(tt.wantDropped) != 0 {
				if !reflect.DeepEqual(got, tt.wantDropped) {
					t.Errorf("DroppedParamsForProvider = %v, want %v", got, tt.wantDropped)
				}
			}

			ctx := WithUnsupportedParamMode(context.Background(), UnsupportedParamReject)
			err := EnforceUnsupportedParams(ctx, "anthropic", "m", tt.req)
			if len(tt.wantDropped) == 0 && err != nil {
				t.Errorf("reject mode returned %v, want nil", err)
			}
			if len(tt.wantDropped) != 0 && err == nil {
				t.Errorf("reject mode returned nil, want error naming %v", tt.wantDropped)
			}
		})
	}
}

// TestPreferCompletionTokens guards the OpenAI-surface providers' outbound field:
// they must always send max_completion_tokens (accepted by every chat model) and
// never max_tokens, which o-series / GPT-5 models reject. A max_tokens-only
// request is promoted rather than dropped so the ceiling survives, and nothing
// is invented when the caller set no limit.
func TestPreferCompletionTokens(t *testing.T) {
	t.Run("promotes a max_tokens-only request to max_completion_tokens", func(t *testing.T) {
		r := Request{MaxTokens: pI(256)}
		r.PreferCompletionTokens()
		if r.MaxTokens != nil {
			t.Errorf("MaxTokens = %v, want nil", *r.MaxTokens)
		}
		if r.MaxCompletionTokens == nil || *r.MaxCompletionTokens != 256 {
			t.Errorf("MaxCompletionTokens = %v, want 256", r.MaxCompletionTokens)
		}
	})

	t.Run("drops max_tokens when both are set", func(t *testing.T) {
		r := Request{MaxTokens: pI(4096), MaxCompletionTokens: pI(4096)}
		r.PreferCompletionTokens()
		if r.MaxTokens != nil {
			t.Errorf("MaxTokens = %v, want nil", *r.MaxTokens)
		}
		if r.MaxCompletionTokens == nil || *r.MaxCompletionTokens != 4096 {
			t.Errorf("MaxCompletionTokens = %v, want 4096", r.MaxCompletionTokens)
		}
	})

	t.Run("invents nothing when neither is set", func(t *testing.T) {
		r := Request{}
		r.PreferCompletionTokens()
		if r.MaxTokens != nil || r.MaxCompletionTokens != nil {
			t.Errorf("both fields must stay nil, got max_tokens=%v max_completion_tokens=%v",
				r.MaxTokens, r.MaxCompletionTokens)
		}
	})
}
