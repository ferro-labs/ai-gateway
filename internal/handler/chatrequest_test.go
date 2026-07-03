package handler

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestDecodeChatCompletionRequest_ParallelToolCalls verifies parallel_tool_calls
// survives HTTP decode into the request and is forwarded on the wire by the
// shared marshal.
func TestDecodeChatCompletionRequest_ParallelToolCalls(t *testing.T) {
	req, err := DecodeChatCompletionRequest(strings.NewReader(
		`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"parallel_tool_calls":false}`))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if req.ParallelToolCalls == nil {
		t.Fatal("parallel_tool_calls not decoded (nil)")
	}
	if *req.ParallelToolCalls {
		t.Errorf("parallel_tool_calls = true, want false")
	}

	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"parallel_tool_calls":false`) {
		t.Errorf("marshaled request missing parallel_tool_calls: %s", b)
	}
}
