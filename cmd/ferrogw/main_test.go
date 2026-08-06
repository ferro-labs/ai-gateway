package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/internal/admin/repository"
	"github.com/ferro-labs/ai-gateway/internal/apierror"
	"github.com/ferro-labs/ai-gateway/internal/bootstrap"
	"github.com/ferro-labs/ai-gateway/internal/handler"
	"github.com/ferro-labs/ai-gateway/internal/httpserver"
	"github.com/ferro-labs/ai-gateway/internal/sse"
	"github.com/ferro-labs/ai-gateway/internal/webui"
	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
)

const (
	testErrTypeServerError = "server_error"
	testCodeRequestReject  = "request_rejected"
)

type fakeProvider struct {
	name   string
	models []string
}

func (f *fakeProvider) Name() string               { return f.name }
func (f *fakeProvider) ConfiguredModels() []string { return f.models }
func (f *fakeProvider) SupportsModel(m string) bool {
	for _, mm := range f.models {
		if mm == m {
			return true
		}
	}
	return false
}
func (f *fakeProvider) Models() []providers.ModelInfo {
	out := make([]providers.ModelInfo, len(f.models))
	for i, m := range f.models {
		out[i] = providers.ModelInfo{ID: m, Object: "model", OwnedBy: f.name}
	}
	return out
}
func (f *fakeProvider) Complete(_ context.Context, _ providers.Request) (*providers.Response, error) {
	return &providers.Response{
		ID:    "fake-id",
		Model: f.models[0],
		Choices: []providers.Choice{{
			Index:        0,
			Message:      providers.Message{Role: "assistant", Content: "hello"},
			FinishReason: "stop",
		}},
	}, nil
}

func testRegistry() *providers.Registry {
	r := providers.NewRegistry()
	r.Register(&fakeProvider{name: "test", models: []string{"test-model"}})
	return r
}

func testKeyStore() *repository.KeyStore {
	return repository.NewKeyStore()
}

func TestHealth(t *testing.T) {
	ks := testKeyStore()
	r := httpserver.NewRouter(testRegistry(), ks, nil, nil, nil, nil, nil, nil, nil, nil, "", nil)
	req := httptest.NewRequestWithContext(t.Context(), "GET", "/health", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode health response: %v", err)
	}
	if _, ok := body["status"]; !ok {
		t.Error("health response missing status field")
	}
	if _, ok := body["providers"]; !ok {
		t.Error("health response missing providers field")
	}
}

func TestModels(t *testing.T) {
	t.Setenv("ALLOW_UNAUTHENTICATED_PROXY", "true")
	ks := testKeyStore()
	r := httpserver.NewRouter(testRegistry(), ks, nil, nil, nil, nil, nil, nil, nil, nil, "", nil)
	req := httptest.NewRequestWithContext(t.Context(), "GET", "/v1/models", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}

	var body map[string]any
	_ = json.NewDecoder(w.Body).Decode(&body)
	if body["object"] != "list" {
		t.Errorf("object = %v", body["object"])
	}
}

func TestPprofDisabledByDefault(t *testing.T) {
	ks := testKeyStore()
	r := httpserver.NewRouter(testRegistry(), ks, nil, nil, nil, nil, nil, nil, nil, nil, "", nil)
	req := httptest.NewRequestWithContext(t.Context(), "GET", "/debug/pprof/", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestPprofEnabledRequiresAuthEvenWhenUnauthenticatedProxyEnabled(t *testing.T) {
	t.Setenv("ENABLE_PPROF", "true")
	t.Setenv("ALLOW_UNAUTHENTICATED_PROXY", "true")
	ks := testKeyStore()
	r := httpserver.NewRouter(testRegistry(), ks, nil, nil, nil, nil, nil, nil, nil, nil, "", nil)
	req := httptest.NewRequestWithContext(t.Context(), "GET", "/debug/pprof/", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401, body = %s", w.Code, w.Body.String())
	}
}

func TestPprofEnabledWithAuth(t *testing.T) {
	t.Setenv("ENABLE_PPROF", "true")
	t.Setenv("ALLOW_UNAUTHENTICATED_PROXY", "true")
	ks := testKeyStore()
	r := httpserver.NewRouter(testRegistry(), ks, nil, nil, nil, nil, nil, nil, nil, nil, "test-master-key", nil)
	req := httptest.NewRequestWithContext(t.Context(), "GET", "/debug/pprof/", nil)
	req.Header.Set("Authorization", "Bearer test-master-key")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "profile") {
		t.Fatalf("expected pprof index response, got: %s", w.Body.String())
	}
}

func TestDebugVarsRequireAuthEvenWhenUnauthenticatedProxyEnabled(t *testing.T) {
	t.Setenv("ALLOW_UNAUTHENTICATED_PROXY", "true")
	ks := testKeyStore()
	r := httpserver.NewRouter(testRegistry(), ks, nil, nil, nil, nil, nil, nil, nil, nil, "", nil)
	req := httptest.NewRequestWithContext(t.Context(), "GET", "/debug/vars", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestMetricsRequireAuthEvenWhenUnauthenticatedProxyEnabled(t *testing.T) {
	t.Setenv("ALLOW_UNAUTHENTICATED_PROXY", "true")
	ks := testKeyStore()
	r := httpserver.NewRouter(testRegistry(), ks, nil, nil, nil, nil, nil, nil, nil, nil, "", nil)
	req := httptest.NewRequestWithContext(t.Context(), "GET", "/metrics", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestDebugVarsEnabledWithAuth(t *testing.T) {
	t.Setenv("ALLOW_UNAUTHENTICATED_PROXY", "true")
	ks := testKeyStore()
	r := httpserver.NewRouter(testRegistry(), ks, nil, nil, nil, nil, nil, nil, nil, nil, "test-master-key", nil)
	req := httptest.NewRequestWithContext(t.Context(), "GET", "/debug/vars", nil)
	req.Header.Set("Authorization", "Bearer test-master-key")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "\"memstats\"") {
		t.Fatalf("expected expvar memstats output, got: %s", w.Body.String())
	}
}

// TestDashboardIsServed covers the dashboard the gateway embeds. The handler
// sits on chi's not-found hook, which is the only reason a client-side route
// like /overview resolves at all — so these cases are really asserting that the
// hook is reached for browser navigation and nothing else.
func TestDashboardIsServed(t *testing.T) {
	ks := testKeyStore()
	r := httpserver.NewRouter(testRegistry(), ks, nil, nil, nil, nil, nil, nil, nil, nil, "", nil)

	const html = "text/html"

	dashboardOK := http.StatusServiceUnavailable
	if webui.Available() {
		dashboardOK = http.StatusOK
	}

	tests := []struct {
		name   string
		method string
		path   string
		accept string
		want   int
	}{
		// dashboardOK, not a literal: whether a bundle is embedded depends on
		// whether `make build` has run in this tree, and a test that flips
		// verdict on that is a false green waiting to happen. Both values mean
		// the handler was reached, which is what this asserts; a 404 would mean
		// the routing never got there.
		{"root", http.MethodGet, "/", html, dashboardOK},
		// Neither path exists as a route; both are React Router destinations
		// that must survive a hard refresh.
		{"client-side route", http.MethodGet, "/overview", html, dashboardOK},
		{"nested client-side route", http.MethodGet, "/keys/new", html, dashboardOK},
		// A caller that did not ask for HTML mistyped a path. Answering with the
		// app shell and a 200 would turn its bug into a JSON parse error a layer
		// away from the cause.
		{"api client gets 404", http.MethodGet, "/overview", "application/json", http.StatusNotFound},
		{"no accept header gets 404", http.MethodGet, "/overview", "", http.StatusNotFound},
		{"write method gets 404", http.MethodPost, "/overview", html, http.StatusNotFound},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, nil)
			if tc.accept != "" {
				req.Header.Set("Accept", tc.accept)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d", w.Code, tc.want)
			}
		})
	}
}

// TestDashboardDoesNotShadowAPI is the risk the not-found placement carries: a
// handler that answers every unmatched path could swallow an API route that
// failed to match for its own reasons, turning a 401 or a 404 into a 200 and an
// HTML body. Each request below asks for HTML, which is the case that would be
// swallowed if the routing precedence were wrong.
func TestDashboardDoesNotShadowAPI(t *testing.T) {
	ks := testKeyStore()
	r := httpserver.NewRouter(testRegistry(), ks, nil, nil, nil, nil, nil, nil, nil, nil, "test-master-key", nil)

	for _, tc := range []struct {
		path    string
		wantNot int
	}{
		{"/health", http.StatusNotFound},
		{"/livez", http.StatusNotFound},
		{"/v1/models", http.StatusNotFound},
		{"/admin/keys", http.StatusOK},
		{"/metrics", http.StatusOK},
	} {
		t.Run(tc.path, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, tc.path, nil)
			req.Header.Set("Accept", "text/html")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if strings.Contains(w.Body.String(), "<html") {
				t.Fatalf("%s served the dashboard shell (status %d): %s", tc.path, w.Code, w.Body.String())
			}
			// /admin and /metrics must reject an unauthenticated caller rather
			// than fall through to the dashboard; the probes must answer.
			if w.Code == tc.wantNot {
				t.Fatalf("%s status = %d, which means it fell through to the not-found handler", tc.path, w.Code)
			}
		})
	}
}

func TestChatCompletions(t *testing.T) {
	t.Setenv("ALLOW_UNAUTHENTICATED_PROXY", "true")
	ks := testKeyStore()
	r := httpserver.NewRouter(testRegistry(), ks, nil, nil, nil, nil, nil, nil, nil, nil, "", nil)
	payload := `{"model":"test-model","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequestWithContext(t.Context(), "POST", "/v1/chat/completions", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200, body = %s", w.Code, w.Body.String())
	}

	var resp providers.Response
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp.ID != "fake-id" {
		t.Errorf("got ID %q", resp.ID)
	}
}

func TestDecodeChatCompletionRequest_MultipartContent(t *testing.T) {
	req, err := handler.DecodeChatCompletionRequest(strings.NewReader(`{
		"model":"test-model",
		"messages":[
			{
				"role":"user",
				"content":[
					{"type":"text","text":"hello "},
					{"type":"image_url","image_url":{"url":"https://example.com/image.png"}},
					{"type":"text","text":"world"}
				]
			}
		]
	}`))
	if err != nil {
		t.Fatalf("handler.DecodeChatCompletionRequest: %v", err)
	}
	if got := len(req.Messages); got != 1 {
		t.Fatalf("messages = %d, want 1", got)
	}
	if req.Messages[0].Content != "hello world" {
		t.Fatalf("content = %q, want %q", req.Messages[0].Content, "hello world")
	}
	if got := len(req.Messages[0].ContentParts); got != 3 {
		t.Fatalf("content parts = %d, want 3", got)
	}
}

func TestDecodeChatCompletionRequest_ToolChoiceString(t *testing.T) {
	req, err := handler.DecodeChatCompletionRequest(strings.NewReader(`{
		"model":"test-model",
		"messages":[{"role":"user","content":"hi"}],
		"tool_choice":"auto"
	}`))
	if err != nil {
		t.Fatalf("handler.DecodeChatCompletionRequest: %v", err)
	}
	if req.ToolChoice != "auto" {
		t.Fatalf("tool_choice = %#v, want %q", req.ToolChoice, "auto")
	}
}

func TestChatCompletions_ValidationError(t *testing.T) {
	t.Setenv("ALLOW_UNAUTHENTICATED_PROXY", "true")
	ks := testKeyStore()
	r := httpserver.NewRouter(testRegistry(), ks, nil, nil, nil, nil, nil, nil, nil, nil, "", nil)
	payload := `{"model":"","messages":[]}`
	req := httptest.NewRequestWithContext(t.Context(), "POST", "/v1/chat/completions", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestChatCompletions_UnsupportedModel(t *testing.T) {
	t.Setenv("ALLOW_UNAUTHENTICATED_PROXY", "true")
	ks := testKeyStore()
	r := httpserver.NewRouter(testRegistry(), ks, nil, nil, nil, nil, nil, nil, nil, nil, "", nil)
	payload := `{"model":"unknown","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequestWithContext(t.Context(), "POST", "/v1/chat/completions", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// 404 is the one answer every routed surface gives for a model this gateway
	// does not serve — see TestSurfaces_UnroutableModel_AgreeOnOneAnswer.
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

type fakeStreamProvider struct {
	fakeProvider
}

func (f *fakeStreamProvider) CompleteStream(_ context.Context, _ providers.Request) (<-chan providers.StreamChunk, error) {
	ch := make(chan providers.StreamChunk, 2)
	ch <- providers.StreamChunk{
		ID:    "stream-1",
		Model: f.models[0],
		Choices: []providers.StreamChoice{{
			Index: 0,
			Delta: providers.MessageDelta{Role: "assistant", Content: "hel"},
		}},
	}
	ch <- providers.StreamChunk{
		ID:    "stream-1",
		Model: f.models[0],
		Choices: []providers.StreamChoice{{
			Index:        0,
			Delta:        providers.MessageDelta{Content: "lo"},
			FinishReason: "stop",
		}},
	}
	close(ch)
	return ch, nil
}

func testStreamRegistry() *providers.Registry {
	r := providers.NewRegistry()
	r.Register(&fakeStreamProvider{fakeProvider{name: "test-stream", models: []string{"test-stream-model"}}})
	return r
}

func TestChatCompletions_Stream(t *testing.T) {
	t.Setenv("ALLOW_UNAUTHENTICATED_PROXY", "true")
	ks := testKeyStore()
	r := httpserver.NewRouter(testStreamRegistry(), ks, nil, nil, nil, nil, nil, nil, nil, nil, "", nil)
	payload := `{"model":"test-stream-model","messages":[{"role":"user","content":"hi"}],"stream":true}`
	req := httptest.NewRequestWithContext(t.Context(), "POST", "/v1/chat/completions", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200, body = %s", w.Code, w.Body.String())
	}
	ct := w.Header().Get("Content-Type")
	if ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, "data: ") {
		t.Errorf("body missing data: lines: %s", body)
	}
	if !strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Errorf("body should end with data: [DONE], got: %s", body)
	}
}

func TestWriteSSE_StreamError(t *testing.T) {
	ch := make(chan providers.StreamChunk, 1)
	ch <- providers.StreamChunk{Error: errors.New("boom")}
	close(ch)

	w := httptest.NewRecorder()
	sse.Write(context.Background(), w, ch, nil)

	body := w.Body.String()
	if !strings.Contains(body, `"type":"stream_error"`) {
		t.Fatalf("expected stream_error payload, got: %s", body)
	}
	if strings.Contains(body, "data: [DONE]") {
		t.Fatalf("did not expect [DONE] after stream error, got: %s", body)
	}
}

func TestChatCompletions_StreamUnsupported(t *testing.T) {
	t.Setenv("ALLOW_UNAUTHENTICATED_PROXY", "true")
	ks := testKeyStore()
	r := httpserver.NewRouter(testRegistry(), ks, nil, nil, nil, nil, nil, nil, nil, nil, "", nil)
	payload := `{"model":"test-model","messages":[{"role":"user","content":"hi"}],"stream":true}`
	req := httptest.NewRequestWithContext(t.Context(), "POST", "/v1/chat/completions", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// A target that cannot stream is not a candidate for a streaming request,
	// which is what a target that cannot embed is for /v1/embeddings. Both are
	// 404 model_not_found; see the streaming comment in internal/handler/chat.go.
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestCreateKeyStoreFromEnv_DefaultMemory(t *testing.T) {
	t.Setenv("API_KEY_STORE_BACKEND", "")
	t.Setenv("API_KEY_STORE_DSN", "")

	store, backend, err := bootstrap.CreateKeyStoreFromEnv(t.Context())
	if err != nil {
		t.Fatalf("bootstrap.CreateKeyStoreFromEnv returned error: %v", err)
	}
	if backend != "memory" {
		t.Fatalf("backend = %s, want memory", backend)
	}
	if _, ok := store.(*repository.KeyStore); !ok {
		t.Fatalf("expected memory KeyStore type")
	}
}

func TestCreateKeyStoreFromEnv_SQLite(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "keys.db")
	t.Setenv("API_KEY_STORE_BACKEND", "sqlite")
	t.Setenv("API_KEY_STORE_DSN", dsn)

	store, backend, err := bootstrap.CreateKeyStoreFromEnv(t.Context())
	if err != nil {
		t.Fatalf("bootstrap.CreateKeyStoreFromEnv returned error: %v", err)
	}
	t.Cleanup(func() {
		if c, ok := store.(io.Closer); ok {
			_ = c.Close()
		}
	})
	if backend != "sqlite" {
		t.Fatalf("backend = %s, want sqlite", backend)
	}

	created, err := store.Create(context.Background(), "test", nil, nil)
	if err != nil {
		t.Fatalf("create key on sqlite store: %v", err)
	}
	if _, ok := store.ValidateKey(context.Background(), created.Key); !ok {
		t.Fatalf("expected created sqlite key to validate")
	}
}

func TestCreateKeyStoreFromEnv_UnknownBackend(t *testing.T) {
	t.Setenv("API_KEY_STORE_BACKEND", "unknown")
	t.Setenv("API_KEY_STORE_DSN", "")

	if _, _, err := bootstrap.CreateKeyStoreFromEnv(t.Context()); err == nil {
		t.Fatalf("expected error for unsupported backend")
	}
}

func TestCreateKeyStoreFromEnv_PostgresMissingDSN(t *testing.T) {
	t.Setenv("API_KEY_STORE_BACKEND", "postgres")
	t.Setenv("API_KEY_STORE_DSN", "")

	if _, _, err := bootstrap.CreateKeyStoreFromEnv(t.Context()); err == nil {
		t.Fatalf("expected error for missing postgres dsn")
	}
}

func TestCreateConfigManagerFromEnv_DefaultMemory(t *testing.T) {
	t.Setenv("CONFIG_STORE_BACKEND", "")
	t.Setenv("CONFIG_STORE_DSN", "")

	gw := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "openai"}},
	})

	mgr, backend, err := bootstrap.CreateConfigManagerFromEnv(t.Context(), gw)
	if err != nil {
		t.Fatalf("bootstrap.CreateConfigManagerFromEnv returned error: %v", err)
	}
	if backend != "memory" {
		t.Fatalf("backend = %s, want memory", backend)
	}
	if cfg := mgr.GetConfig(); cfg.Strategy.Mode != config.ModeSingle {
		t.Fatalf("unexpected config mode: %s", cfg.Strategy.Mode)
	}
}

func TestCreateConfigManagerFromEnv_SQLitePersistence(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "config.db")
	t.Setenv("CONFIG_STORE_BACKEND", "sqlite")
	t.Setenv("CONFIG_STORE_DSN", dsn)

	initialCfg := config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "openai"}},
	}
	updatedCfg := config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeFallback},
		Targets: []config.Target{
			{VirtualKey: "openai"},
			{VirtualKey: "anthropic"},
		},
	}

	gw1 := newTestGateway(t, initialCfg)
	mgr1, backend, err := bootstrap.CreateConfigManagerFromEnv(t.Context(), gw1)
	if err != nil {
		t.Fatalf("bootstrap.CreateConfigManagerFromEnv returned error: %v", err)
	}
	if backend != "sqlite" {
		t.Fatalf("backend = %s, want sqlite", backend)
	}
	if err := mgr1.ReloadConfig(context.Background(), updatedCfg); err != nil {
		t.Fatalf("reload config via manager: %v", err)
	}

	gw2 := newTestGateway(t, initialCfg)
	mgr2, _, err := bootstrap.CreateConfigManagerFromEnv(t.Context(), gw2)
	if err != nil {
		t.Fatalf("bootstrap.CreateConfigManagerFromEnv (second) returned error: %v", err)
	}
	loaded := mgr2.GetConfig()
	if loaded.Strategy.Mode != config.ModeFallback {
		t.Fatalf("expected persisted fallback mode, got %s", loaded.Strategy.Mode)
	}
	if len(loaded.Targets) != 2 {
		t.Fatalf("expected persisted 2 targets, got %d", len(loaded.Targets))
	}
}

func TestCreateConfigManagerFromEnv_UnknownBackend(t *testing.T) {
	t.Setenv("CONFIG_STORE_BACKEND", "unknown")
	t.Setenv("CONFIG_STORE_DSN", "")

	gw := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "openai"}},
	})

	if _, _, err := bootstrap.CreateConfigManagerFromEnv(t.Context(), gw); err == nil {
		t.Fatalf("expected error for unsupported backend")
	}
}

func TestCreateConfigManagerFromEnv_PostgresMissingDSN(t *testing.T) {
	t.Setenv("CONFIG_STORE_BACKEND", "postgres")
	t.Setenv("CONFIG_STORE_DSN", "")

	gw := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "openai"}},
	})

	if _, _, err := bootstrap.CreateConfigManagerFromEnv(t.Context(), gw); err == nil {
		t.Fatalf("expected error for missing postgres dsn")
	}
}

func TestNewHTTPServer_SetsHardeningTimeouts(t *testing.T) {
	srv := httpserver.NewServer(":8080", http.NewServeMux())
	if srv.ReadTimeout != httpserver.ServerReadTimeout {
		t.Fatalf("ReadTimeout = %v, want %v", srv.ReadTimeout, httpserver.ServerReadTimeout)
	}
	if srv.ReadHeaderTimeout != httpserver.ServerReadHeaderTimeout {
		t.Fatalf("ReadHeaderTimeout = %v, want %v", srv.ReadHeaderTimeout, httpserver.ServerReadHeaderTimeout)
	}
	if srv.WriteTimeout != httpserver.ServerWriteTimeout {
		t.Fatalf("WriteTimeout = %v, want %v", srv.WriteTimeout, httpserver.ServerWriteTimeout)
	}
	if srv.IdleTimeout != httpserver.ServerIdleTimeout {
		t.Fatalf("IdleTimeout = %v, want %v", srv.IdleTimeout, httpserver.ServerIdleTimeout)
	}
	if srv.MaxHeaderBytes != httpserver.ServerMaxHeaderBytes {
		t.Fatalf("MaxHeaderBytes = %d, want %d", srv.MaxHeaderBytes, httpserver.ServerMaxHeaderBytes)
	}
}

func TestCloseResources_AggregatesCloserErrors(t *testing.T) {
	err := httpserver.CloseResources(
		httpserver.NamedResource{Name: "first", Value: testCloser{err: errors.New("boom one")}},
		httpserver.NamedResource{Name: "second", Value: testCloser{err: errors.New("boom two")}},
	)
	if err == nil {
		t.Fatal("expected aggregated close error")
	}
	if !strings.Contains(err.Error(), "close first: boom one") {
		t.Fatalf("missing first close error: %v", err)
	}
	if !strings.Contains(err.Error(), "close second: boom two") {
		t.Fatalf("missing second close error: %v", err)
	}
}

func TestRouteErrorDetails_BeforeRequestRejection(t *testing.T) {
	status, errType, code := apierror.RouteErrorDetails(&plugin.RejectionError{Stage: plugin.StageBeforeRequest, Reason: "blocked"})
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", status, http.StatusBadRequest)
	}
	if errType != "invalid_request_error" {
		t.Fatalf("errType = %q, want %q", errType, "invalid_request_error")
	}
	if code != testCodeRequestReject {
		t.Fatalf("code = %q, want %q", code, testCodeRequestReject)
	}
}

func TestRouteErrorDetails_RateLimitRejection_Returns429(t *testing.T) {
	err := &plugin.RejectionError{
		PluginType: plugin.TypeRateLimit,
		Stage:      plugin.StageBeforeRequest,
		Reason:     "budget exceeded",
	}
	status, errType, code := apierror.RouteErrorDetails(err)
	if status != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", status, http.StatusTooManyRequests)
	}
	if errType != "rate_limit_error" {
		t.Fatalf("errType = %q, want %q", errType, "rate_limit_error")
	}
	if code != "rate_limit_exceeded" {
		t.Fatalf("code = %q, want %q", code, "rate_limit_exceeded")
	}
}

func TestRouteErrorDetails_AfterRequestRejection(t *testing.T) {
	status, errType, code := apierror.RouteErrorDetails(&plugin.RejectionError{Stage: plugin.StageAfterRequest, Reason: "schema mismatch"})
	if status != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", status, http.StatusBadGateway)
	}
	if errType != "upstream_error" {
		t.Fatalf("errType = %q, want %q", errType, "upstream_error")
	}
	if code != "response_rejected" {
		t.Fatalf("code = %q, want %q", code, "response_rejected")
	}
}

func TestRouteErrorDetails_UnknownStageRejection(t *testing.T) {
	status, errType, code := apierror.RouteErrorDetails(&plugin.RejectionError{Stage: plugin.Stage("custom_stage"), Reason: "custom"})
	if status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", status, http.StatusInternalServerError)
	}
	if errType != testErrTypeServerError {
		t.Fatalf("errType = %q, want %q", errType, testErrTypeServerError)
	}
	if code != testCodeRequestReject {
		t.Fatalf("code = %q, want %q", code, testCodeRequestReject)
	}
}

func TestRouteErrorDetails_NonRejectionError(t *testing.T) {
	status, errType, code := apierror.RouteErrorDetails(errors.New("boom"))
	if status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", status, http.StatusInternalServerError)
	}
	if errType != testErrTypeServerError {
		t.Fatalf("errType = %q, want %q", errType, testErrTypeServerError)
	}
	if code != "routing_error" {
		t.Fatalf("code = %q, want %q", code, "routing_error")
	}
}

func newTestGateway(t *testing.T, cfg config.Config) *aigateway.Gateway {
	t.Helper()
	gw, err := aigateway.New(cfg)
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	t.Cleanup(func() {
		if err := gw.Close(); err != nil {
			t.Errorf("close gateway: %v", err)
		}
	})
	return gw
}

type testCloser struct {
	err error
}

func (c testCloser) Close() error {
	return c.err
}
