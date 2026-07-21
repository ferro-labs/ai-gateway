package core

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestRequest_ClientStreamOptions_NeverMarshaled locks in the invariant the
// whole A11/B1 fix depends on: ClientStreamOptions (the client's raw
// stream_options) must never appear on any wire body built by marshaling a
// core.Request directly — that is exactly how the ~20 OpenAI-compatible
// providers in providers/internal/openaicompat build their upstream request,
// and forwarding a client's explicit include_usage:false there would disable
// their usage reporting silently. Only providers/openai builds its own
// separate wire type that always forces include_usage:true.
func TestRequest_ClientStreamOptions_NeverMarshaled(t *testing.T) {
	req := Request{
		Model:               "gpt-4o",
		Messages:            []Message{{Role: "user", Content: "hi"}},
		ClientStreamOptions: &StreamOptions{IncludeUsage: false},
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "ClientStreamOptions") || strings.Contains(string(b), "include_usage") {
		t.Fatalf("marshaled request leaked ClientStreamOptions onto the wire: %s", b)
	}
}

// TestStreamOptions_IncludeUsageFalse_RoundTrips verifies an explicit false
// is distinguishable from an omitted stream_options object wherever
// StreamOptions itself (not embedded in Request) is marshaled directly —
// the classic omitempty-swallows-false footgun the reference fix flagged.
func TestStreamOptions_IncludeUsageFalse_RoundTrips(t *testing.T) {
	b, err := json.Marshal(StreamOptions{IncludeUsage: false})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(b) != `{"include_usage":false}` {
		t.Fatalf("marshaled = %s, want explicit false (not swallowed by omitempty)", b)
	}
}
