package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// A scenario can name the completion, so a test can make the upstream answer
// with exactly the text a response-side check has to see.
func TestScenario_ContentReplacesTheCompletion(t *testing.T) {
	srv := newTestServer(t)
	setScenario(t, srv, `{"content":"{\"name\":\"ada\"}"}`)

	r := completion(t, srv, `{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	if r.status != http.StatusOK {
		t.Fatalf("status %d", r.status)
	}
	got := decode(t, bytes.NewReader(r.body))
	content := got["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"]
	if content != `{"name":"ada"}` {
		t.Fatalf("content = %q, want the scenario's", content)
	}

	s := completion(t, srv, `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	var streamed strings.Builder
	for line := range strings.SplitSeq(string(s.body), "\n") {
		if !strings.HasPrefix(line, "data: {") {
			continue
		}
		var chunk map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk); err != nil {
			t.Fatalf("chunk: %v", err)
		}
		delta := chunk["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)
		if c, ok := delta["content"].(string); ok {
			streamed.WriteString(c)
		}
	}
	if streamed.String() != `{"name":"ada"}` {
		t.Fatalf("streamed content = %q, want the scenario's", streamed.String())
	}
}

// /_mock/calls remembers the last prompt, so a test can prove what the
// gateway forwarded — a rewritten request, or one a non-blocking action let
// through — rather than infer it from the status code.
func TestCalls_RememberTheLastPrompt(t *testing.T) {
	srv := newTestServer(t)
	setScenario(t, srv, `{}`)

	completion(t, srv, `{"model":"m","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"my ssn is [REDACTED]"}]}`)

	got := calls(t, srv)
	if got["last_prompt"] != "be brief\nmy ssn is [REDACTED]" {
		t.Fatalf("last_prompt = %q", got["last_prompt"])
	}

	call(t, srv, http.MethodPost, "/_mock/reset", "")
	if p := calls(t, srv)["last_prompt"]; p != "" {
		t.Fatalf("last_prompt after reset = %q, want empty", p)
	}
}
