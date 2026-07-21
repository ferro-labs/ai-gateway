package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/logging"
	"github.com/ferro-labs/ai-gateway/internal/streamio"
	"github.com/ferro-labs/ai-gateway/providers"
	openaipkg "github.com/ferro-labs/ai-gateway/providers/openai"
)

type nonProxyProvider struct {
	name    string
	models  []string
	resp    *providers.Response
	calls   int
	lastReq providers.Request
}

func (m *nonProxyProvider) Name() string                  { return m.name }
func (m *nonProxyProvider) SupportedModels() []string     { return m.models }
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
	if m.resp != nil {
		return m.resp, nil
	}
	return &providers.Response{
		ID:    "np-1",
		Model: "non-proxy-model",
		Choices: []providers.Choice{{
			Index:        0,
			Message:      providers.Message{Role: providers.RoleAssistant, Content: "ok"},
			FinishReason: "stop",
		}},
	}, nil
}

// proxiableBadURLProvider is a ProxiableProvider whose BaseURL is deliberately
// malformed, used to exercise the CompletionsEndpointURL error path without
// depending on a real provider constructor's own URL validation.
type proxiableBadURLProvider struct {
	nonProxyProvider
	baseURL string
}

func (p *proxiableBadURLProvider) BaseURL() string                { return p.baseURL }
func (p *proxiableBadURLProvider) AuthHeaders() map[string]string { return nil }

func TestCompletionsEndpointURL(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		want    string
		wantErr bool
	}{
		{name: "root base URL", baseURL: "https://api.openai.com", want: "https://api.openai.com/v1/completions"},
		{name: "already has v1", baseURL: "https://api.example.com/v1", want: "https://api.example.com/v1/completions"},
		{name: "already has v1 trailing slash", baseURL: "https://api.example.com/v1/", want: "https://api.example.com/v1/completions"},
		{name: "invalid", baseURL: "not a url", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CompletionsEndpointURL(tt.baseURL)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for base URL %q", tt.baseURL)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCompletionsHandler_ProxyPath_DoesNotDuplicateV1(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"cmpl-1","object":"text_completion","model":"gpt-4o","choices":[{"text":"ok","index":0,"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	reg := providers.NewRegistry()
	op, err := openaipkg.New("sk-test", upstream.URL+"/v1")
	if err != nil {
		t.Fatalf("failed to build openai provider: %v", err)
	}
	reg.Register(op)

	h := Completions(reg)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/completions", strings.NewReader(`{"model":"gpt-4o","prompt":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if gotPath != "/v1/completions" {
		t.Fatalf("expected upstream path /v1/completions, got %q", gotPath)
	}
}

func TestCompletionsHandler_ShimsStreamRequest_ReturnsExplicitError(t *testing.T) {
	np := &nonProxyProvider{name: "non-proxy", models: []string{"non-proxy-model"}}

	reg := providers.NewRegistry()
	reg.Register(np)

	h := Completions(reg)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/completions", strings.NewReader(`{"model":"non-proxy-model","prompt":"hi","stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}

	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if payload.Error.Code != "streaming_not_supported" {
		t.Fatalf("expected error code streaming_not_supported, got %q", payload.Error.Code)
	}
	if np.calls != 0 {
		t.Fatalf("provider should not be called for unsupported stream shim, got %d calls", np.calls)
	}
}

// TestCompletionsHandler_ShimPromptForms covers every OpenAI-valid `prompt`
// shape through the chat shim (Path 2): a bare string and a single-element
// array are representable as one text message and must be accepted; a
// multi-element array (batch), token-id arrays, and arrays of token-id
// arrays cannot be represented as a single chat message and must be
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &nonProxyProvider{name: "shim", models: []string{"legacy-model"}}
			reg := providers.NewRegistry()
			reg.Register(p)
			body := `{"model":"legacy-model","prompt":` + tt.promptJSON + `}`
			w := httptest.NewRecorder()
			Completions(reg)(w, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/completions", strings.NewReader(body)))

			if !tt.wantOK {
				if w.Code != http.StatusBadRequest {
					t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
				}
				var payload struct {
					Error struct {
						Code string `json:"code"`
					} `json:"error"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
					t.Fatalf("decode error response: %v", err)
				}
				if payload.Error.Code != "unsupported_parameter" {
					t.Fatalf("error code = %q, want unsupported_parameter", payload.Error.Code)
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
// (bare string, array of strings) through the chat shim, asserting they
// normalize into the []string form providers.Request expects.
func TestCompletionsHandler_ShimStopForms(t *testing.T) {
	tests := []struct {
		name     string
		stopJSON string
		want     []string
	}{
		{"bare string", `"\n\n"`, []string{"\n\n"}},
		{"array of strings", `["a","b"]`, []string{"a", "b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &nonProxyProvider{name: "shim", models: []string{"legacy-model"}}
			reg := providers.NewRegistry()
			reg.Register(p)
			body := `{"model":"legacy-model","prompt":"hi","stop":` + tt.stopJSON + `}`
			w := httptest.NewRecorder()
			Completions(reg)(w, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/completions", strings.NewReader(body)))

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
			}
			if !reflect.DeepEqual(p.lastReq.Stop, tt.want) {
				t.Fatalf("stop = %#v, want %#v", p.lastReq.Stop, tt.want)
			}
		})
	}
}

// TestCompletionsHandler_NativeProxyAcceptsAllPromptAndStopForms is the
// regression test for the shipped bug: Path 1 forwards the request body
// verbatim to a provider that natively supports every OpenAI prompt/stop
// shape, so json.Unmarshal into LegacyCompletionRequest must never reject a
// valid body before the proxy ever sees it.
func TestCompletionsHandler_NativeProxyAcceptsAllPromptAndStopForms(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"bare string prompt", `{"model":"gpt-4o","prompt":"hi"}`},
		{"single-element array prompt", `{"model":"gpt-4o","prompt":["hi"]}`},
		{"multi-element array prompt batch", `{"model":"gpt-4o","prompt":["hi","there"]}`},
		{"token id prompt", `{"model":"gpt-4o","prompt":[1,2,3]}`},
		{"array of token-id arrays prompt", `{"model":"gpt-4o","prompt":[[1,2],[3,4]]}`},
		{"bare string stop", `{"model":"gpt-4o","prompt":"hi","stop":"\n\n"}`},
		{"array stop", `{"model":"gpt-4o","prompt":"hi","stop":["a","b"]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotBody []byte
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotBody, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"id":"cmpl-1","object":"text_completion","model":"gpt-4o","choices":[{"text":"ok","index":0,"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
			}))
			defer upstream.Close()

			reg := providers.NewRegistry()
			op, err := openaipkg.New("sk-test", upstream.URL+"/v1")
			if err != nil {
				t.Fatalf("failed to build openai provider: %v", err)
			}
			reg.Register(op)

			h := Completions(reg)
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/completions", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			h(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (verbatim forward must accept every valid prompt/stop shape): %s", w.Code, w.Body.String())
			}
			if string(gotBody) != tt.body {
				t.Fatalf("upstream body = %s, want verbatim %s", gotBody, tt.body)
			}
		})
	}
}

// TestCompletionsHandler_ShimIgnoresLegacyOnlyParams is the deferral guard:
// echo/best_of/logprobs/suffix must keep the v1.3.0 behavior of being
// decoded and silently ignored on the chat-shim path. Turning them into a
// 400 is a /v1 breaking change deferred to a later minor release — this
// test fails if that rejection creeps back in.
func TestCompletionsHandler_ShimIgnoresLegacyOnlyParams(t *testing.T) {
	tests := []struct {
		name  string
		field string
	}{
		{"logprobs", `"logprobs":2`},
		{"echo", `"echo":true`},
		{"best_of", `"best_of":2`},
		{"suffix", `"suffix":"done"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &nonProxyProvider{name: "shim", models: []string{"legacy-model"}}
			reg := providers.NewRegistry()
			reg.Register(p)
			body := `{"model":"legacy-model","prompt":"hi",` + tt.field + `}`
			w := httptest.NewRecorder()
			Completions(reg)(w, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/completions", strings.NewReader(body)))
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (this field must remain ignored, not rejected): %s", w.Code, w.Body.String())
			}
			if p.calls != 1 {
				t.Fatalf("provider calls = %d, want 1", p.calls)
			}
		})
	}
}

// TestCompletionsHandler_ShimResponseIncludesCreatedAndNullLogprobs asserts
// the shim response envelope matches the real OpenAI completions object:
// `created` populated from the underlying chat response, and `logprobs`
// present as an explicit null (not an omitted key) on every choice.
func TestCompletionsHandler_ShimResponseIncludesCreatedAndNullLogprobs(t *testing.T) {
	p := &nonProxyProvider{
		name:   "shim",
		models: []string{"legacy-model"},
		resp: &providers.Response{
			ID:      "cmpl-created",
			Model:   "legacy-model",
			Created: 1234567890,
			Choices: []providers.Choice{{
				Index:        0,
				Message:      providers.Message{Role: providers.RoleAssistant, Content: "ok"},
				FinishReason: "stop",
			}},
		},
	}
	reg := providers.NewRegistry()
	reg.Register(p)

	w := httptest.NewRecorder()
	Completions(reg)(w, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/completions", strings.NewReader(
		`{"model":"legacy-model","prompt":"hi"}`)))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
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

// TestCompletionsHandler_NativeProxyURLErrorDoesNotLeakBaseURL is the
// regression test for the base-URL credential leak: a malformed operator-
// configured base URL (which may carry a secret in a query string on a
// self-hosted OpenAI-compatible proxy) must never reach the client verbatim.
func TestCompletionsHandler_NativeProxyURLErrorDoesNotLeakBaseURL(t *testing.T) {
	secret := "sk-" + strings.Repeat("b", 48)
	p := &proxiableBadURLProvider{
		nonProxyProvider: nonProxyProvider{name: "bad-url", models: []string{"bad-url-model"}},
		baseURL:          "not-a-url?api_key=" + secret,
	}
	reg := providers.NewRegistry()
	reg.Register(p)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/completions", strings.NewReader(`{"model":"bad-url-model","prompt":"hi"}`))
	w := httptest.NewRecorder()
	Completions(reg)(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), secret) {
		t.Fatalf("completions URL error leaked provider base URL secret: %s", w.Body.String())
	}
}

func TestCompletionsHandler_ProxyStreamPathUsesWriteDeadlines(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"id\":\"chunk-1\"}\n\n"))
	}))
	defer upstream.Close()

	reg := providers.NewRegistry()
	op, err := openaipkg.New("sk-test", upstream.URL)
	if err != nil {
		t.Fatalf("failed to build openai provider: %v", err)
	}
	reg.Register(op)

	h := Completions(reg)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/completions", strings.NewReader(`{"model":"gpt-4o","prompt":"hi","stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	w := newCompletionDeadlineRecorder()

	h(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "data:") {
		t.Fatalf("expected streamed body, got %q", w.Body.String())
	}
	if len(w.deadlines) < 2 {
		t.Fatalf("expected write deadline set and clear, got %d entries", len(w.deadlines))
	}
	if w.deadlines[0].IsZero() {
		t.Fatal("first deadline should set a timeout")
	}
	if !w.deadlines[len(w.deadlines)-1].IsZero() {
		t.Fatalf("last deadline should clear timeout, got %v", w.deadlines[len(w.deadlines)-1])
	}
	if w.flushes == 0 {
		t.Fatal("expected streaming response flush")
	}
}

// Cutting a stalled upstream must not be silent: the copy error is the only
// evidence the idle bound fired, and a client that hung up is not an error.
func TestCompletionsHandler_StalledUpstreamCutIsLogged(t *testing.T) {
	defer streamio.SetIdleTimeoutForTest(50 * time.Millisecond)()

	var logs bytes.Buffer
	prevLogger := logging.Logger
	logging.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	defer func() { logging.Logger = prevLogger }()

	upstreamDone := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"id\":\"chunk-1\"}\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done() // stall until the gateway gives up
		close(upstreamDone)
	}))
	defer upstream.Close()

	reg := providers.NewRegistry()
	op, err := openaipkg.New("sk-test", upstream.URL)
	if err != nil {
		t.Fatalf("failed to build openai provider: %v", err)
	}
	reg.Register(op)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/completions",
		strings.NewReader(`{"model":"gpt-4o","prompt":"hi","stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	w := newCompletionDeadlineRecorder()

	Completions(reg)(w, req)

	select {
	case <-upstreamDone:
	case <-time.After(10 * time.Second):
		t.Fatal("upstream request was never cancelled by the idle bound")
	}

	if got := logs.String(); !strings.Contains(got, "completions response copy failed") {
		t.Fatalf("idle-timeout cut was not logged: %s", got)
	}
}

type completionDeadlineRecorder struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
	flushes   int
}

func newCompletionDeadlineRecorder() *completionDeadlineRecorder {
	return &completionDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
}

func (r *completionDeadlineRecorder) Flush() {
	r.flushes++
	r.ResponseRecorder.Flush()
}

func (r *completionDeadlineRecorder) SetWriteDeadline(deadline time.Time) error {
	r.deadlines = append(r.deadlines, deadline)
	return nil
}
