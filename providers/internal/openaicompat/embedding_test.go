package openaicompat

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

const embeddingSuccessBody = `{
	"object":"list",
	"model":"text-embedding-3-small",
	"data":[{"object":"embedding","index":0,"embedding":[0.1,0.2,0.3]}],
	"usage":{"prompt_tokens":5,"total_tokens":5}
}`

// TestPostEmbeddings_Success exercises the request-building path (method, headers
// from params, normalized body), the 2xx success-range check, and response
// unmarshalling. It also asserts a non-200 2xx (202) still decodes — PostEmbeddings
// accepts the whole 2xx range, unlike PostChat which requires exactly 200.
func TestPostEmbeddings_Success(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusAccepted} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var (
				gotMethod string
				gotHeader http.Header
				gotBody   map[string]json.RawMessage
			)

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				gotHeader = r.Header.Clone()
				raw, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(raw, &gotBody)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = w.Write([]byte(embeddingSuccessBody))
			}))
			defer srv.Close()

			p := EmbeddingParams{
				HTTPClient: srv.Client(),
				URL:        srv.URL,
				Headers: map[string]string{
					"Authorization": "Bearer test-key",
					"Content-Type":  "application/json",
				},
				Label: "testprov",
			}
			req := core.EmbeddingRequest{
				Model: "text-embedding-3-small",
				Input: "hello world",
			}

			resp, err := PostEmbeddings(context.Background(), p, req)
			if err != nil {
				t.Fatalf("PostEmbeddings returned error: %v", err)
			}

			// Request path assertions.
			if gotMethod != http.MethodPost {
				t.Errorf("method = %q, want POST", gotMethod)
			}
			if got := gotHeader.Get("Authorization"); got != "Bearer test-key" {
				t.Errorf("Authorization header = %q, want %q", got, "Bearer test-key")
			}
			if got := gotHeader.Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type header = %q, want application/json", got)
			}
			if string(gotBody["model"]) != `"text-embedding-3-small"` {
				t.Errorf("body model = %s, want %q", gotBody["model"], "text-embedding-3-small")
			}
			// A bare string Input must stay a JSON string on the wire.
			if string(gotBody["input"]) != `"hello world"` {
				t.Errorf("body input = %s, want bare string %q", gotBody["input"], "hello world")
			}

			// Response unmarshalling assertions.
			if resp.Object != "list" || resp.Model != "text-embedding-3-small" {
				t.Errorf("resp object/model = %q/%q, want list/text-embedding-3-small", resp.Object, resp.Model)
			}
			if len(resp.Data) != 1 {
				t.Fatalf("resp.Data len = %d, want 1", len(resp.Data))
			}
			if resp.Data[0].Index != 0 || !reflect.DeepEqual(resp.Data[0].Embedding, []float64{0.1, 0.2, 0.3}) {
				t.Errorf("resp.Data[0] = %#v, want index 0 with [0.1 0.2 0.3]", resp.Data[0])
			}
			if resp.Usage.PromptTokens != 5 || resp.Usage.TotalTokens != 5 {
				t.Errorf("resp.Usage = %#v, want prompt/total 5/5", resp.Usage)
			}
		})
	}
}

// TestPostEmbeddings_ArrayInputForwarded confirms a []string Input is forwarded
// as a JSON array (not flattened), matching normalizeEmbeddingInput's contract.
func TestPostEmbeddings_ArrayInputForwarded(t *testing.T) {
	var gotInput json.RawMessage

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		gotInput = body["input"]
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(embeddingSuccessBody))
	}))
	defer srv.Close()

	p := EmbeddingParams{HTTPClient: srv.Client(), URL: srv.URL, Label: "testprov"}
	req := core.EmbeddingRequest{Model: "m", Input: []string{"a", "b"}}

	if _, err := PostEmbeddings(context.Background(), p, req); err != nil {
		t.Fatalf("PostEmbeddings returned error: %v", err)
	}
	if string(gotInput) != `["a","b"]` {
		t.Errorf("body input = %s, want array %q", gotInput, `["a","b"]`)
	}
}

// TestPostEmbeddings_APIError pins the non-2xx path: the drained body is surfaced
// via APIError using the OpenAI {"error":{"message":…}} envelope.
func TestPostEmbeddings_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	defer srv.Close()

	p := EmbeddingParams{
		HTTPClient: srv.Client(),
		URL:        srv.URL,
		Headers:    map[string]string{"Content-Type": "application/json"},
		Label:      "acme",
	}

	_, err := PostEmbeddings(context.Background(), p, core.EmbeddingRequest{Model: "m", Input: "hi"})
	if err == nil {
		t.Fatal("expected error on HTTP 429, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "acme API error (429)") {
		t.Errorf("error = %q, want it to contain %q", msg, "acme API error (429)")
	}
	if !strings.Contains(msg, "rate limited") {
		t.Errorf("error = %q, want it to contain provider message %q", msg, "rate limited")
	}
}

// TestPostEmbeddings_RejectsInvalidInput checks that a bad Input short-circuits
// before any HTTP call is made (normalizeEmbeddingInput error is returned).
func TestPostEmbeddings_RejectsInvalidInput(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := EmbeddingParams{HTTPClient: srv.Client(), URL: srv.URL, Label: "testprov"}
	_, err := PostEmbeddings(context.Background(), p, core.EmbeddingRequest{Model: "m", Input: nil})
	if err == nil {
		t.Fatal("expected error for nil Input, got nil")
	}
	if called {
		t.Error("HTTP endpoint was called; invalid input should short-circuit before the request")
	}
}

// TestNormalizeEmbeddingInput is the table-driven contract for the polymorphic
// Input: bare string and []string keep their wire form, []any of strings is
// coerced to []string, and empty arrays / nil / unsupported types are rejected.
func TestNormalizeEmbeddingInput(t *testing.T) {
	tests := []struct {
		name    string
		input   any
		want    any
		wantErr bool
	}{
		{name: "bare string preserved", input: "hello", want: "hello"},
		{name: "empty string is valid", input: "", want: ""},
		{name: "string slice preserved", input: []string{"a", "b"}, want: []string{"a", "b"}},
		{name: "any slice of strings coerced", input: []any{"a", "b"}, want: []string{"a", "b"}},
		{name: "empty string slice rejected", input: []string{}, wantErr: true},
		{name: "empty any slice rejected", input: []any{}, wantErr: true},
		{name: "any slice with non-string rejected", input: []any{"a", 1}, wantErr: true},
		{name: "nil rejected", input: nil, wantErr: true},
		{name: "unsupported type rejected", input: 42, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeEmbeddingInput(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (result=%#v)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}
