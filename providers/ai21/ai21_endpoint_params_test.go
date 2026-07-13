package ai21

import (
	"context"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// TestEndpointScopedParamSupport guards the dual-path nature of AI21.
//
// AI21 advertises only Jamba models, which speak the OpenAI-compatible chat API
// and DO support tools/response_format/seed. The four-parameter limit
// (max_tokens/temperature/top_p/stop) belongs solely to the deprecated Jurassic
// /complete endpoint. Declaring that limit provider-wide in the capability matrix
// silently stripped `tools` from every Jamba request under drop mode and rejected
// it with a 400 under reject mode.
func TestEndpointScopedParamSupport(t *testing.T) {
	rejectCtx := core.WithUnsupportedParamMode(context.Background(), core.UnsupportedParamReject)
	req := core.Request{
		Model: "jamba-mini-1.7",
		Tools: []core.Tool{{Type: "function"}},
	}

	// Jamba: the matrix must NOT mark tools unsupported for ai21.
	if err := core.EnforceUnsupportedParams(rejectCtx, Name, req.Model, req); err != nil {
		t.Errorf("Jamba supports tools; reject mode must not fire: %v", err)
	}

	// Jurassic: the endpoint genuinely cannot express tools, and says so explicitly.
	err := core.EnforceUnsupportedParamsList(rejectCtx, Name, "j2-ultra", req,
		"max_tokens", "temperature", "top_p", "stop")
	if err == nil {
		t.Error("the Jurassic endpoint cannot express tools; reject mode must fire there")
	}
}
