package mistral

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// servedModel is a Mistral model id whose name carries a vendor-family prefix
// ("open-mistral-") rather than the plain "mistral-" one. A remap that stripped
// or normalised the prefix would still produce a model this provider claims to
// support, so the defect it would cause is invisible to SupportsModel and only
// observable on the wire.
const servedModel = "open-mistral-nemo"

// captureOutboundModel decodes the model id from a serialized chat request body
// and reports it alongside the raw bytes. The raw bytes are kept so a caller can
// assert on the encoded form, not just on a decoded map: this test exists to pin
// what actually leaves the process.
func captureOutboundModel(t *testing.T, raw []byte) string {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode outbound body: %v (body=%s)", err, raw)
	}
	model, ok := body["model"]
	if !ok {
		t.Fatalf("outbound body carries no %q field: %s", "model", raw)
	}
	got, ok := model.(string)
	if !ok {
		t.Fatalf("outbound model = %#v, want a string: %s", model, raw)
	}
	return got
}

// TestMistralProvider_OutboundModelID_PassesThroughVerbatim asserts that the
// model id a caller asks for is the model id Mistral is asked for, on both chat
// surfaces. The assertion is on the serialized request body the stub upstream
// received — not on core.Request or any provider field — because the wire body
// is the only thing the upstream reads.
func TestMistralProvider_OutboundModelID_PassesThroughVerbatim(t *testing.T) {
	chatResponse := `{"id":"chatcmpl-1","model":"` + servedModel + `","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
	streamResponse := "data: {\"id\":\"chatcmpl-1\",\"model\":\"" + servedModel + "\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"

	tests := []struct {
		name string
		// respond writes the stub upstream's reply for this surface.
		respond func(w http.ResponseWriter)
		// call drives the provider surface under test and drains any stream so
		// the request has completed by the time the body is inspected.
		call func(t *testing.T, p *Provider, req core.Request)
	}{
		{
			name: "Complete",
			respond: func(w http.ResponseWriter) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(chatResponse))
			},
			call: func(t *testing.T, p *Provider, req core.Request) {
				t.Helper()
				if _, err := p.Complete(context.Background(), req); err != nil {
					t.Fatalf("Complete() error: %v", err)
				}
			},
		},
		{
			name: "CompleteStream",
			respond: func(w http.ResponseWriter) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(streamResponse))
			},
			call: func(t *testing.T, p *Provider, req core.Request) {
				t.Helper()
				ch, err := p.CompleteStream(context.Background(), req)
				if err != nil {
					t.Fatalf("CompleteStream() error: %v", err)
				}
				for chunk := range ch {
					if chunk.Error != nil {
						t.Fatalf("stream chunk error: %v", chunk.Error)
					}
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var raw []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read request body: %v", err)
				}
				raw = body
				tt.respond(w)
			}))
			defer srv.Close()

			p, err := New("test-key", srv.URL)
			if err != nil {
				t.Fatalf("New() error: %v", err)
			}
			tt.call(t, p, core.Request{
				Model:    servedModel,
				Messages: []core.Message{{Role: "user", Content: "Hi"}},
			})

			if got := captureOutboundModel(t, raw); got != servedModel {
				t.Errorf("outbound model = %q, want %q (the provider remapped the served model id)", got, servedModel)
			}
		})
	}
}
