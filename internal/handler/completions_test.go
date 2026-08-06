package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"

	_ "github.com/ferro-labs/ai-gateway/plugin/wordfilter"
)

const legacyTestModel = "legacy-model"

type nonProxyProvider struct {
	name    string
	models  []string
	resp    *providers.Response
	err     error
	calls   int
	lastReq providers.Request
}

func (m *nonProxyProvider) Name() string                  { return m.name }
func (m *nonProxyProvider) ConfiguredModels() []string    { return m.models }
func (m *nonProxyProvider) Models() []providers.ModelInfo { return nil }
func (m *nonProxyProvider) SupportsModel(model string) bool {
	for _, mm := range m.models {
		if mm == model {
			return true
		}
	}
	return false
}
func (m *nonProxyProvider) Complete(_ context.Context, req providers.Request) (*providers.Response, error) {
	m.calls++
	m.lastReq = req
	if m.err != nil {
		return nil, m.err
	}
	if m.resp != nil {
		return m.resp, nil
	}
	return &providers.Response{
		ID:    "np-1",
		Model: req.Model,
		Choices: []providers.Choice{{
			Index:        0,
			Message:      providers.Message{Role: providers.RoleAssistant, Content: "ok"},
			FinishReason: "stop",
		}},
	}, nil
}

// proxiableProvider is a provider that also advertises a pass-through base URL,
// i.e. one that could in principle serve /v1/completions natively upstream.
type proxiableProvider struct {
	*nonProxyProvider
}

func (p *proxiableProvider) BaseURL() string                { return "https://upstream.invalid/v1" }
func (p *proxiableProvider) AuthHeaders() map[string]string { return nil }

// nativeWireProvider is a proxiable provider whose upstream API is not
// OpenAI-wire-compatible (Anthropic, Gemini, Bedrock, Cohere, Vertex, Azure).
type nativeWireProvider struct {
	proxiableProvider
}

func (p *nativeWireProvider) NonOpenAIWire() {}

// completionsGateway builds a gateway from cfg, registers ps on it, and loads
// any plugins cfg declares.
func completionsGateway(t *testing.T, cfg config.Config, ps ...providers.Provider) *aigateway.Gateway {
	t.Helper()
	gw, err := newTestGateway(t, cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, p := range ps {
		gw.RegisterProvider(p)
	}
	if err := gw.LoadPlugins(); err != nil {
		t.Fatalf("LoadPlugins: %v", err)
	}
	return gw
}

// shimGateway is the common single-target setup: one provider serving
// legacyTestModel, listed under targets.
func shimGateway(t *testing.T, p providers.Provider) *aigateway.Gateway {
	t.Helper()
	return completionsGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeFallback},
		Targets:  []config.Target{{VirtualKey: p.Name()}},
	}, p)
}

func postCompletions(t *testing.T, gw *aigateway.Gateway, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	Completions(gw)(w, req)
	return w
}

func errorCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode error response: %v (body=%s)", err, w.Body.String())
	}
	return payload.Error.Code
}

// TestCompletionsHandler_PluginBlocksLikeChat is the regression test for the
// pipeline bypass: an operator who configures a guardrail and verifies it on
// /v1/chat/completions must get the same answer for the same prompt on
// /v1/completions. Serving it from the provider registry meant the identical
// request was answered unfiltered here.
func TestCompletionsHandler_PluginBlocksLikeChat(t *testing.T) {
	p := &nonProxyProvider{name: "shim", models: []string{legacyTestModel}}
	gw := completionsGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeFallback},
		Targets:  []config.Target{{VirtualKey: "shim"}},
		Plugins: []config.PluginConfig{{
			Name:    "word-filter",
			Type:    "guardrail",
			Stage:   "before_request",
			Enabled: true,
			Config:  map[string]any{"blocked_words": []any{"forbidden"}},
		}},
	}, p)

	chat := httptest.NewRecorder()
	ChatCompletions(gw)(chat, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"`+legacyTestModel+`","messages":[{"role":"user","content":"a forbidden thing"}]}`)))
	if chat.Code != http.StatusBadRequest {
		t.Fatalf("chat status = %d, want 400 (guardrail must block): %s", chat.Code, chat.Body.String())
	}

	w := postCompletions(t, gw, `{"model":"`+legacyTestModel+`","prompt":"a forbidden thing"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("completions status = %d, want 400 (same guardrail, same prompt): %s", w.Code, w.Body.String())
	}
	if got := errorCode(t, w); got != "request_rejected" {
		t.Fatalf("error code = %q, want request_rejected", got)
	}
	if p.calls != 0 {
		t.Fatalf("provider calls = %d, want 0 — the guardrail must run before the provider", p.calls)
	}
}

// TestCompletionsHandler_ServesOnlyConfiguredTargets pins the access-control
// half of the same bypass: a provider whose credential is configured but which
// an operator deliberately left out of `targets` must not be reachable here.
func TestCompletionsHandler_ServesOnlyConfiguredTargets(t *testing.T) {
	t.Run("listed target is served", func(t *testing.T) {
		p := &nonProxyProvider{name: "listed", models: []string{legacyTestModel}}
		gw := completionsGateway(t, config.Config{
			Strategy: config.StrategyConfig{Mode: config.ModeFallback},
			Targets:  []config.Target{{VirtualKey: "listed"}},
		}, p)

		w := postCompletions(t, gw, `{"model":"`+legacyTestModel+`","prompt":"hi"}`)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
		}
		if p.calls != 1 {
			t.Fatalf("provider calls = %d, want 1", p.calls)
		}
	})

	t.Run("excluded provider is not served", func(t *testing.T) {
		listed := &nonProxyProvider{name: "listed", models: []string{"listed-model"}}
		excluded := &nonProxyProvider{name: "excluded", models: []string{"excluded-model"}}
		gw := completionsGateway(t, config.Config{
			Strategy: config.StrategyConfig{Mode: config.ModeFallback},
			Targets:  []config.Target{{VirtualKey: "listed"}},
		}, listed, excluded)

		w := postCompletions(t, gw, `{"model":"excluded-model","prompt":"hi"}`)
		if w.Code == http.StatusOK {
			t.Fatalf("a provider excluded from targets was served: %s", w.Body.String())
		}
		if excluded.calls != 0 {
			t.Fatalf("excluded provider calls = %d, want 0", excluded.calls)
		}
	})
}

// TestCompletionsHandler_ResolvesAlias verifies a configured alias routes here
// the way it routes on chat. The handler saw only a provider registry before,
// which cannot see config.Aliases at all.
func TestCompletionsHandler_ResolvesAlias(t *testing.T) {
	p := &nonProxyProvider{name: "shim", models: []string{legacyTestModel}}
	gw := completionsGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeFallback},
		Targets:  []config.Target{{VirtualKey: "shim"}},
		Aliases:  map[string]string{"fast": legacyTestModel},
	}, p)

	w := postCompletions(t, gw, `{"model":"fast","prompt":"hi"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if p.lastReq.Model != legacyTestModel {
		t.Fatalf("provider saw model %q, want the alias target %q", p.lastReq.Model, legacyTestModel)
	}
}

// TestCompletionsHandler_ProxiableProvidersUseTheShim covers the providers that
// used to be forwarded verbatim to an upstream /v1/completions route. A
// non-OpenAI-wire upstream has no such route, so the raw forward returned the
// provider's own 404 in a provider-shaped body; every provider now goes through
// the translated chat path instead.
func TestCompletionsHandler_ProxiableProvidersUseTheShim(t *testing.T) {
	tests := []struct {
		name string
		of   func(*nonProxyProvider) providers.Provider
	}{
		{"openai-wire proxiable", func(base *nonProxyProvider) providers.Provider {
			return &proxiableProvider{base}
		}},
		{"native-wire proxiable", func(base *nonProxyProvider) providers.Provider {
			return &nativeWireProvider{proxiableProvider{base}}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := &nonProxyProvider{name: "proxiable", models: []string{legacyTestModel}}
			gw := shimGateway(t, tt.of(base))

			w := postCompletions(t, gw, `{"model":"`+legacyTestModel+`","prompt":"hi"}`)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
			}
			var payload legacyResponse
			if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if payload.Object != "text_completion" {
				t.Fatalf("object = %q, want text_completion", payload.Object)
			}
			if base.calls != 1 {
				t.Fatalf("provider Complete calls = %d, want 1 (the shim, not a native forward)", base.calls)
			}
		})
	}
}

// TestCompletionsHandler_UpstreamStatusIsPreserved pins the shared error
// classification: a throttled upstream stays a 429 and carries its Retry-After,
// rather than being flattened into a 500.
func TestCompletionsHandler_UpstreamStatusIsPreserved(t *testing.T) {
	upstream := &core.HTTPStatusError{
		StatusCode: http.StatusTooManyRequests,
		Message:    "shim API error (429): rate limited",
		RetryAfter: 7 * time.Second,
	}
	p := &nonProxyProvider{name: "shim", models: []string{legacyTestModel}, err: upstream}
	gw := shimGateway(t, p)

	w := postCompletions(t, gw, `{"model":"`+legacyTestModel+`","prompt":"hi"}`)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Retry-After"); got != "7" {
		t.Fatalf("Retry-After = %q, want 7", got)
	}
	if got := errorCode(t, w); got != "rate_limit_exceeded" {
		t.Fatalf("error code = %q, want rate_limit_exceeded", got)
	}
}

// The status is the router's, not this handler's: 404 is what every routed
// surface answers for a model the gateway does not serve. See
// TestSurfaces_UnroutableModel_AgreeOnOneAnswer.
func TestCompletionsHandler_UnknownModelIsRejected(t *testing.T) {
	gw := shimGateway(t, &nonProxyProvider{name: "shim", models: []string{legacyTestModel}})

	w := postCompletions(t, gw, `{"model":"no-such-model","prompt":"hi"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", w.Code, w.Body.String())
	}
	if got := errorCode(t, w); got != "model_not_found" {
		t.Fatalf("error code = %q, want model_not_found", got)
	}
}

// Streaming has no legacy envelope through the gateway, so it is refused
// outright rather than left to hang or return an empty 200.
func TestCompletionsHandler_StreamRequestIsRefused(t *testing.T) {
	p := &nonProxyProvider{name: "shim", models: []string{legacyTestModel}}
	gw := shimGateway(t, p)

	w := postCompletions(t, gw, `{"model":"`+legacyTestModel+`","prompt":"hi","stream":true}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
	if got := errorCode(t, w); got != "streaming_not_supported" {
		t.Fatalf("error code = %q, want streaming_not_supported", got)
	}
	if p.calls != 0 {
		t.Fatalf("provider calls = %d, want 0", p.calls)
	}
}

// TestCompletionsHandler_ShimPromptForms covers every OpenAI-valid `prompt`
// shape: a bare string and a single-element array are representable as one
// chat message and must be accepted; a multi-element array (batch), token-id
// arrays, and arrays of token-id arrays cannot be represented and must be
// rejected with a 400, not silently mangled.
func TestCompletionsHandler_ShimPromptForms(t *testing.T) {
	tests := []struct {
		name       string
		promptJSON string
		wantOK     bool
		wantText   string
	}{
		{"bare string", `"hi"`, true, "hi"},
		{"single-element array", `["hi"]`, true, "hi"},
		{"multi-element array batch", `["hi","there"]`, false, ""},
		{"token ids", `[1,2,3]`, false, ""},
		{"array of token-id arrays", `[[1,2],[3,4]]`, false, ""},
		// A bare null decodes into a string as a no-op, which is the behaviour
		// this endpoint has always had for "prompt": null.
		{"explicit null", `null`, true, ""},
		// A null element would decode to "" the same way, silently turning the
		// request into an empty prompt rather than reporting that there is no
		// text to send.
		{"single-element array holding null", `[null]`, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &nonProxyProvider{name: "shim", models: []string{legacyTestModel}}
			gw := shimGateway(t, p)
			w := postCompletions(t, gw, `{"model":"`+legacyTestModel+`","prompt":`+tt.promptJSON+`}`)

			if !tt.wantOK {
				if w.Code != http.StatusBadRequest {
					t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
				}
				if got := errorCode(t, w); got != "unsupported_parameter" {
					t.Fatalf("error code = %q, want unsupported_parameter", got)
				}
				if p.calls != 0 {
					t.Fatalf("provider called despite unsupported prompt shape")
				}
				return
			}

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
			}
			if p.calls != 1 {
				t.Fatalf("provider calls = %d, want 1", p.calls)
			}
			if len(p.lastReq.Messages) != 1 || p.lastReq.Messages[0].Content != tt.wantText {
				t.Fatalf("shim message = %+v, want content %q", p.lastReq.Messages, tt.wantText)
			}
		})
	}
}

// TestCompletionsHandler_ShimStopForms covers both OpenAI-valid `stop` shapes
// (bare string, array of strings), asserting they normalize into the []string
// form providers.Request expects.
func TestCompletionsHandler_ShimStopForms(t *testing.T) {
	tests := []struct {
		name     string
		stopJSON string
		want     []string
	}{
		{"bare string", `"\n\n"`, []string{"\n\n"}},
		{"array of strings", `["a","b"]`, []string{"a", "b"}},
		// null means "no stop sequences". Decoding it into a string yields ""
		// and would send a single empty stop sequence upstream, which is a
		// different request. Must stay nil.
		{"explicit null", `null`, nil},
		// A null inside the array has always decoded to "", so that is left
		// alone; changing it would alter a request the gateway already accepts.
		{"array containing null", `["a",null]`, []string{"a", ""}},
	}
	// Shapes that are not stop sequences at all. Silently becoming "no stop
	// sequences" would hand the caller a longer completion than they asked for
	// with nothing to indicate why.
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
			gw := shimGateway(t, &nonProxyProvider{name: "shim", models: []string{legacyTestModel}})
			w := postCompletions(t, gw, `{"model":"`+legacyTestModel+`","prompt":"hi","stop":`+tt.stopJSON+`}`)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 for stop=%s: %s", w.Code, tt.stopJSON, w.Body.String())
			}
		})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &nonProxyProvider{name: "shim", models: []string{legacyTestModel}}
			gw := shimGateway(t, p)
			w := postCompletions(t, gw, `{"model":"`+legacyTestModel+`","prompt":"hi","stop":`+tt.stopJSON+`}`)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
			}
			if !reflect.DeepEqual(p.lastReq.Stop, tt.want) {
				t.Fatalf("stop = %#v, want %#v", p.lastReq.Stop, tt.want)
			}
		})
	}
}

// choiceTexts reads choices[].text out of a legacy completions response.
func choiceTexts(t *testing.T, w *httptest.ResponseRecorder) []string {
	t.Helper()
	var payload struct {
		Choices []struct {
			Text string `json:"text"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, w.Body.String())
	}
	texts := make([]string, 0, len(payload.Choices))
	for _, c := range payload.Choices {
		texts = append(texts, c.Text)
	}
	return texts
}

// TestCompletionsHandler_ShimIgnoresLegacyOnlyParams pins the compatibility
// contract for best_of/logprobs/suffix: they are decoded and ignored, never
// rejected. Refusing them would turn a request that callers send today into a
// 400, so this test fails if that creeps in.
//
// It also asserts the returned text is the completion and nothing else. suffix
// is the one of the three that LOOKS appendable — it is not: OpenAI treats it
// as text the model generates INTO and returns only the bridging middle, so
// appending it here would hand back text no model wrote. echo is honoured and
// covered separately below.
func TestCompletionsHandler_ShimIgnoresLegacyOnlyParams(t *testing.T) {
	tests := []struct {
		name  string
		field string
	}{
		{"logprobs", `"logprobs":2`},
		{"best_of", `"best_of":2`},
		{"suffix", `"suffix":"done"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &nonProxyProvider{name: "shim", models: []string{legacyTestModel}}
			gw := shimGateway(t, p)
			w := postCompletions(t, gw, `{"model":"`+legacyTestModel+`","prompt":"hi",`+tt.field+`}`)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (this field must remain ignored, not rejected): %s", w.Code, w.Body.String())
			}
			if p.calls != 1 {
				t.Fatalf("provider calls = %d, want 1", p.calls)
			}
			if got := choiceTexts(t, w); !reflect.DeepEqual(got, []string{"ok"}) {
				t.Fatalf("text = %q, want the completion alone — %s is accepted and dropped, not rendered", got, tt.name)
			}
		})
	}
}

// TestCompletionsHandler_EchoReturnsPromptPlusCompletion covers the one legacy
// field the chat shim can express exactly. OpenAI documents echo as "echo back
// the prompt in addition to the completion", which is a statement about the
// response envelope alone: each choice's text becomes prompt+completion, the
// request that goes upstream is untouched, and with n>1 every candidate is
// echoed because every candidate is its own completion.
func TestCompletionsHandler_EchoReturnsPromptPlusCompletion(t *testing.T) {
	const prompt = "Once upon a time"

	twoChoices := func() *providers.Response {
		return &providers.Response{
			ID:    "cmpl-echo",
			Model: legacyTestModel,
			Choices: []providers.Choice{
				{Index: 0, Message: providers.Message{Role: providers.RoleAssistant, Content: " there was a cat."}, FinishReason: "stop"},
				{Index: 1, Message: providers.Message{Role: providers.RoleAssistant, Content: " there was a dog."}, FinishReason: "stop"},
			},
		}
	}

	tests := []struct {
		name  string
		field string
		want  []string
	}{
		{
			name:  "echo true prepends the prompt to every choice",
			field: `,"echo":true`,
			want:  []string{prompt + " there was a cat.", prompt + " there was a dog."},
		},
		{
			name:  "echo false returns the completion alone",
			field: `,"echo":false`,
			want:  []string{" there was a cat.", " there was a dog."},
		},
		{
			name:  "echo absent returns the completion alone",
			field: ``,
			want:  []string{" there was a cat.", " there was a dog."},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &nonProxyProvider{name: "shim", models: []string{legacyTestModel}, resp: twoChoices()}
			gw := shimGateway(t, p)

			w := postCompletions(t, gw, `{"model":"`+legacyTestModel+`","prompt":"`+prompt+`"`+tt.field+`}`)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
			}
			if got := choiceTexts(t, w); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("choices[].text = %q, want %q", got, tt.want)
			}
			// echo describes the response. Nothing about it may reach the
			// provider, or a guardrail and a bill would see a prompt the
			// caller did not send.
			if len(p.lastReq.Messages) != 1 || p.lastReq.Messages[0].Content != prompt {
				t.Fatalf("upstream messages = %#v, want the prompt unchanged", p.lastReq.Messages)
			}
		})
	}
}

// TestCompletionsHandler_ShimResponseIncludesCreatedAndNullLogprobs asserts
// the response envelope matches the real OpenAI completions object: `created`
// populated from the underlying chat response, and `logprobs` present as an
// explicit null (not an omitted key) on every choice.
func TestCompletionsHandler_ShimResponseIncludesCreatedAndNullLogprobs(t *testing.T) {
	p := &nonProxyProvider{
		name:   "shim",
		models: []string{legacyTestModel},
		resp: &providers.Response{
			ID:      "cmpl-created",
			Model:   legacyTestModel,
			Created: 1234567890,
			Choices: []providers.Choice{{
				Index:        0,
				Message:      providers.Message{Role: providers.RoleAssistant, Content: "ok"},
				FinishReason: "stop",
			}},
		},
	}
	gw := shimGateway(t, p)

	w := postCompletions(t, gw, `{"model":"`+legacyTestModel+`","prompt":"hi"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("X-Gateway-Provider"); got != "shim" {
		t.Fatalf("X-Gateway-Provider = %q, want the routed target name", got)
	}

	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got, want := payload["created"], float64(1234567890); got != want {
		t.Fatalf("created = %v, want %v", got, want)
	}
	choices, ok := payload["choices"].([]any)
	if !ok || len(choices) != 1 {
		t.Fatalf("unexpected choices: %v", payload["choices"])
	}
	choice, ok := choices[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected choice shape: %v", choices[0])
	}
	logprobs, hasKey := choice["logprobs"]
	if !hasKey {
		t.Fatal("choice missing logprobs key, want explicit null")
	}
	if logprobs != nil {
		t.Fatalf("logprobs = %v, want null", logprobs)
	}
}
