package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// TestDecodeChatCompletionRequest_StopForms pins `stop` as the union OpenAI
// documents — a bare string OR an array of strings — on the chat surface.
// Typing the field []string made `"stop": "END"` a 400 here while
// /v1/completions accepted it, for the same field of the same API.
func TestDecodeChatCompletionRequest_StopForms(t *testing.T) {
	const head = `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]`

	accepted := []struct {
		name     string
		stopJSON string
		want     []string
	}{
		{"bare string", `"END"`, []string{"END"}},
		{"bare string with escapes", `"\n\n"`, []string{"\n\n"}},
		{"array of strings", `["a","b"]`, []string{"a", "b"}},
		{"empty array", `[]`, []string{}},
		// null means "no stop sequences" — it must not become a single empty
		// stop sequence, which is a different request upstream.
		{"explicit null", `null`, nil},
	}
	for _, tt := range accepted {
		t.Run(tt.name, func(t *testing.T) {
			req, err := DecodeChatCompletionRequest(strings.NewReader(head + `,"stop":` + tt.stopJSON + `}`))
			if err != nil {
				t.Fatalf("decode stop=%s: %v", tt.stopJSON, err)
			}
			if !reflect.DeepEqual(req.Stop, tt.want) {
				t.Fatalf("stop = %#v, want %#v", req.Stop, tt.want)
			}
		})
	}

	t.Run("absent", func(t *testing.T) {
		req, err := DecodeChatCompletionRequest(strings.NewReader(head + `}`))
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if req.Stop != nil {
			t.Fatalf("stop = %#v, want nil when absent", req.Stop)
		}
	})

	rejected := []struct {
		name     string
		stopJSON string
	}{
		{"number", `42`},
		{"boolean", `true`},
		{"object", `{"a":1}`},
		{"array holding a number", `["a",7]`},
	}
	for _, tt := range rejected {
		t.Run("rejects "+tt.name, func(t *testing.T) {
			if _, err := DecodeChatCompletionRequest(strings.NewReader(head + `,"stop":` + tt.stopJSON + `}`)); err == nil {
				t.Fatalf("stop=%s decoded without error, want rejection", tt.stopJSON)
			}
		})
	}
}

// TestDecodeChatCompletionRequest_TrailingContent verifies a body carrying more
// than one JSON document is refused. json.Decoder reads a stream, so it used to
// decode the first document and silently discard the rest.
func TestDecodeChatCompletionRequest_TrailingContent(t *testing.T) {
	const first = `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`

	rejected := []struct {
		name string
		body string
	}{
		{"second document", first + `{"model":"other","messages":[]}`},
		{"trailing garbage", first + ` oops`},
		{"trailing array", first + `[1,2]`},
		{"trailing brace", first + `}`},
	}
	for _, tt := range rejected {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := DecodeChatCompletionRequest(strings.NewReader(tt.body)); err == nil {
				t.Fatalf("body %q decoded without error, want rejection", tt.body)
			}
		})
	}

	t.Run("trailing whitespace is accepted", func(t *testing.T) {
		req, err := DecodeChatCompletionRequest(strings.NewReader(first + "\n\t  \n"))
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if req.Model != "gpt-4" {
			t.Fatalf("model = %q, want gpt-4", req.Model)
		}
	})
}

// TestDecodeChatCompletionRequest_BodyTooLargeAfterDocument keeps the 413
// classification working on the read the trailing-content guard performs. The
// first document here is complete and inside the limit, so the size cap is
// reached only by that extra read — the one path where an *http.MaxBytesError
// could be reported as a plain 400 instead.
func TestDecodeChatCompletionRequest_BodyTooLargeAfterDocument(t *testing.T) {
	body := `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}` + strings.Repeat(" ", 500)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(body))
	w := httptest.NewRecorder()
	req.Body = http.MaxBytesReader(w, req.Body, int64(len(body)-400))

	_, err := DecodeChatCompletionRequest(req.Body)
	if err == nil {
		t.Fatal("expected error from oversized body, got nil")
	}
	var maxBytesErr *http.MaxBytesError
	if !errors.As(err, &maxBytesErr) {
		t.Fatalf("errors.As(*http.MaxBytesError) returned false for %T: %v", err, err)
	}
}

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
