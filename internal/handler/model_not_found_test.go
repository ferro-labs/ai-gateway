package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/config"
)

// TestSurfaces_UnroutableModel_AgreeOnOneAnswer is the drift guard between the
// routed surfaces on the one condition every client hits first: this gateway
// does not serve that model.
//
// The four disagreed. /v1/chat/completions and /v1/completions each checked the
// model against the REGISTERED provider set before routing and answered 400
// "no provider supports model: X"; /v1/embeddings and /v1/images/generations
// had no such check and answered with the router's own 404 "no configured
// provider serves the requested model". The registered set is not the routable
// one — targets is an allowlist — so the pre-flight was also asking a different
// question than the router that followed it.
//
// 404 is the answer kept, and the pre-flight checks are gone. It is the status
// the OpenAI contract gives an unknown model, and it is the one an SDK does not
// retry: openai-go retries 408, 409, 429 and 5xx, so a 400 was at least honest
// about not retrying, but a 500 or a 400 both send an operator looking in the
// wrong place. An unroutable model is the caller's to fix, and no amount of
// retrying changes it.
func TestSurfaces_UnroutableModel_AgreeOnOneAnswer(t *testing.T) {
	// The provider IS registered and DOES serve ghostModel — it is simply not
	// named by any target. That is the case the pre-flight checks got wrong, and
	// it makes this test non-vacuous: a check against the registered set would
	// admit the model here and only fail further in.
	const (
		ghostModel   = "ghost-model"
		unknownModel = "no-such-model-anywhere"
	)
	gw := completionsGateway(t,
		config.Config{
			Strategy: config.StrategyConfig{Mode: config.ModeSingle},
			Targets:  []config.Target{{VirtualKey: "targeted"}},
		},
		&nonProxyProvider{name: "untargeted", models: []string{ghostModel}},
	)

	surfaces := []struct {
		name    string
		path    string
		body    func(model string) string
		handler http.HandlerFunc
	}{
		{
			name:    "chat completions",
			path:    "/v1/chat/completions",
			body:    func(m string) string { return `{"model":"` + m + `","messages":[{"role":"user","content":"hi"}]}` },
			handler: ChatCompletions(gw),
		},
		{
			name: "streaming chat completions",
			path: "/v1/chat/completions",
			body: func(m string) string {
				return `{"model":"` + m + `","messages":[{"role":"user","content":"hi"}],"stream":true}`
			},
			handler: ChatCompletions(gw),
		},
		{
			name:    "legacy completions",
			path:    "/v1/completions",
			body:    func(m string) string { return `{"model":"` + m + `","prompt":"hi"}` },
			handler: Completions(gw),
		},
		{
			name:    "embeddings",
			path:    "/v1/embeddings",
			body:    func(m string) string { return `{"model":"` + m + `","input":"hi"}` },
			handler: Embeddings(gw),
		},
		{
			name:    "image generations",
			path:    "/v1/images/generations",
			body:    func(m string) string { return `{"model":"` + m + `","prompt":"hi"}` },
			handler: Images(gw),
		},
	}

	// Both shapes of "not served here" must land on the same answer. A model
	// some registered provider serves and a model nothing serves differ only in
	// gateway internals; to the caller they are one condition.
	models := map[string]string{
		"registered but not a target": ghostModel,
		"unknown to every provider":   unknownModel,
	}

	for modelCase, model := range models {
		for _, s := range surfaces {
			t.Run(modelCase+"/"+s.name, func(t *testing.T) {
				r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, s.path, strings.NewReader(s.body(model)))
				r.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()

				s.handler(w, r)

				if w.Code != http.StatusNotFound {
					t.Fatalf("status = %d, want 404 (body: %s)", w.Code, w.Body.String())
				}
				var resp struct {
					Error struct {
						Message string `json:"message"`
						Type    string `json:"type"`
						Code    string `json:"code"`
					} `json:"error"`
				}
				if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
					t.Fatalf("decode: %v", err)
				}
				if resp.Error.Code != "model_not_found" || resp.Error.Type != "invalid_request_error" {
					t.Errorf("type/code = %q/%q, want invalid_request_error/model_not_found", resp.Error.Type, resp.Error.Code)
				}
				if resp.Error.Message != "no configured provider serves the requested model" {
					t.Errorf("message = %q, want the one answer every surface gives", resp.Error.Message)
				}
			})
		}
	}
}
