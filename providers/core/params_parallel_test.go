package core

import (
	"context"
	"errors"
	"slices"
	"testing"
)

// The matrix declaring a parameter Unsupported is only half of enforcement. The
// other half is that the gateway can see the caller populated it: DroppedParams
// and DroppedParamsForProvider both walk optionalParamOrder and ask
// ParamPopulated, so a parameter missing from either list is declared
// unsupported and then enforced for nobody — no warning, no drop, no reject,
// and the caller's intent silently discarded exactly as before.
//
// These tests assert the enforcement fires, not that the published JSON changed.

// TestParamPopulated_ParallelToolCalls pins the pointer semantics: an explicit
// false is the value that actually needs reporting, so "populated" must mean
// "the caller stated a preference", not "the preference is true".
func TestParamPopulated_ParallelToolCalls(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name string
		req  Request
		want bool
	}{
		{"absent", Request{}, false},
		{"explicit false", Request{ParallelToolCalls: &no}, true},
		{"explicit true", Request{ParallelToolCalls: &yes}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParamPopulated(tc.req, "parallel_tool_calls"); got != tc.want {
				t.Errorf("ParamPopulated(parallel_tool_calls) = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestOptionalParamOrder_CoversParallelToolCalls fails if parallel_tool_calls is
// dropped from the ordering DroppedParams* iterates. A matrix entry outside that
// list is inert.
func TestOptionalParamOrder_CoversParallelToolCalls(t *testing.T) {
	if !slices.Contains(optionalParamOrder, "parallel_tool_calls") {
		t.Fatal("optionalParamOrder omits parallel_tool_calls: the matrix entries " +
			"declaring it Unsupported are inert and it reaches every provider unchecked")
	}
}

// TestEnforceUnsupportedParams_ParallelToolCallsRejects proves the reject mode
// actually fires for the providers that cannot express the parameter.
func TestEnforceUnsupportedParams_ParallelToolCallsRejects(t *testing.T) {
	no := false
	req := Request{
		Model:             "m",
		Messages:          []Message{{Role: RoleUser, Content: "hi"}},
		Tools:             []Tool{{Type: "function", Function: Function{Name: "f"}}},
		ParallelToolCalls: &no,
	}
	ctx := WithUnsupportedParamMode(context.Background(), UnsupportedParamReject)

	for _, provider := range []string{"gemini", "cohere", "replicate"} {
		t.Run(provider, func(t *testing.T) {
			dropped := DroppedParamsForProvider(req, provider)
			if !slices.Contains(dropped, "parallel_tool_calls") {
				t.Fatalf("DroppedParamsForProvider(%q) = %v, want it to name parallel_tool_calls",
					provider, dropped)
			}
			err := EnforceUnsupportedParams(ctx, provider, "m", req)
			var unsupported *UnsupportedParamError
			if !errors.As(err, &unsupported) {
				t.Fatalf("EnforceUnsupportedParams(%q) = %v, want *UnsupportedParamError", provider, err)
			}
			if !slices.Contains(unsupported.Params, "parallel_tool_calls") {
				t.Errorf("error params = %v, want parallel_tool_calls", unsupported.Params)
			}
		})
	}
}

// TestEnforceUnsupportedParams_ParallelToolCallsTranslatedNotRejected is the
// other half: anthropic and bedrock map the parameter onto Anthropic's
// disable_parallel_tool_use, so declaring it Unsupported there would reject
// requests the gateway serves correctly.
//
// bedrock is asserted through both seams because it uses both: the provider-level
// matrix is what /v1/capabilities publishes, while enforcement runs per-model
// through EnforceUnsupportedParamsList (only the Anthropic families have tool
// calling at all).
func TestEnforceUnsupportedParams_ParallelToolCallsTranslatedNotRejected(t *testing.T) {
	no := false
	req := Request{
		Model:             "m",
		Messages:          []Message{{Role: RoleUser, Content: "hi"}},
		Tools:             []Tool{{Type: "function", Function: Function{Name: "f"}}},
		ParallelToolCalls: &no,
	}
	ctx := WithUnsupportedParamMode(context.Background(), UnsupportedParamReject)

	for _, provider := range []string{"anthropic", "bedrock"} {
		t.Run(provider, func(t *testing.T) {
			if dropped := DroppedParamsForProvider(req, provider); slices.Contains(dropped, "parallel_tool_calls") {
				t.Fatalf("DroppedParamsForProvider(%q) = %v, want parallel_tool_calls absent (it is translated)",
					provider, dropped)
			}
		})
	}
	if err := EnforceUnsupportedParams(ctx, "anthropic", "m", req); err != nil {
		t.Fatalf("EnforceUnsupportedParams(anthropic) = %v, want nil", err)
	}
	// The Bedrock Anthropic family's supported set, as providers/bedrock resolves
	// it per model. parallel_tool_calls must be in it, or the translation the
	// matrix advertises is rejected before it can run.
	err := EnforceUnsupportedParamsList(ctx, "bedrock", "anthropic.claude-3", req,
		"temperature", "top_p", "max_tokens", "stop", "tools", "tool_choice", "parallel_tool_calls")
	if err != nil {
		t.Fatalf("EnforceUnsupportedParamsList(bedrock anthropic family) = %v, want nil", err)
	}
	// And the families without tool calling still report it, since nothing there
	// can carry the caller's intent.
	err = EnforceUnsupportedParamsList(ctx, "bedrock", "meta.llama3", req,
		"temperature", "top_p", "max_tokens")
	var unsupported *UnsupportedParamError
	if !errors.As(err, &unsupported) || !slices.Contains(unsupported.Params, "parallel_tool_calls") {
		t.Fatalf("EnforceUnsupportedParamsList(bedrock llama) = %v, want it to name parallel_tool_calls", err)
	}
}

// TestEnforceUnsupportedParams_ParallelToolCallsUnsetIsNotReported guards the
// obvious over-reach: a request that never mentioned the parameter must not be
// rejected by a provider that cannot express it.
func TestEnforceUnsupportedParams_ParallelToolCallsUnsetIsNotReported(t *testing.T) {
	req := Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}}
	ctx := WithUnsupportedParamMode(context.Background(), UnsupportedParamReject)
	if err := EnforceUnsupportedParams(ctx, "gemini", "m", req); err != nil {
		t.Fatalf("EnforceUnsupportedParams = %v, want nil for a request that set nothing", err)
	}
}
