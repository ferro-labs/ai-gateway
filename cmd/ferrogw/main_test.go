package main

import (
	"bytes"
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
	"github.com/ferro-labs/ai-gateway/internal/admin"
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

func (f *fakeProvider) Name() string              { return f.name }
func (f *fakeProvider) SupportedModels() []string { return f.models }
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

func testKeyStore() *admin.KeyStore {
	return admin.NewKeyStore()
}

func TestHealth(t *testing.T) {
	ks := testKeyStore()
	r := newRouter(testRegistry(), ks, nil, nil, nil, nil, nil, nil, "")
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	var body map[string]interface{}
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
	r := newRouter(testRegistry(), ks, nil, nil, nil, nil, nil, nil, "")
	req := httptest.NewRequest("GET", "/v1/models", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}

	var body map[string]interface{}
	_ = json.NewDecoder(w.Body).Decode(&body)
	if body["object"] != "list" {
		t.Errorf("object = %v", body["object"])
	}
}

func TestPprofDisabledByDefault(t *testing.T) {
	ks := testKeyStore()
	r := newRouter(testRegistry(), ks, nil, nil, nil, nil, nil, nil, "")
	req := httptest.NewRequest("GET", "/debug/pprof/", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestPprofEnabled(t *testing.T) {
	t.Setenv("ENABLE_PPROF", "true")
	t.Setenv("ALLOW_UNAUTHENTICATED_PROXY", "true")
	ks := testKeyStore()
	r := newRouter(testRegistry(), ks, nil, nil, nil, nil, nil, nil, "")
	req := httptest.NewRequest("GET", "/debug/pprof/", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "profile") {
		t.Fatalf("expected pprof index response, got: %s", w.Body.String())
	}
}

func TestDebugVarsEnabled(t *testing.T) {
	t.Setenv("ALLOW_UNAUTHENTICATED_PROXY", "true")
	ks := testKeyStore()
	r := newRouter(testRegistry(), ks, nil, nil, nil, nil, nil, nil, "")
	req := httptest.NewRequest("GET", "/debug/vars", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "\"memstats\"") {
		t.Fatalf("expected expvar memstats output, got: %s", w.Body.String())
	}
}

func TestDashboardUIPage(t *testing.T) {
	ks := testKeyStore()
	r := newRouter(testRegistry(), ks, nil, nil, nil, nil, nil, nil, "")
	tests := []struct {
		path  string
		title string
	}{
		{"/dashboard/getting-started", "Getting Started"},
		{"/dashboard/overview", "Overview"},
		{"/dashboard/keys", "API Keys"},
		{"/dashboard/logs", "Request Logs"},
		{"/dashboard/providers", "Providers"},
		{"/dashboard/config", "Config"},
		{"/dashboard/analytics", "Analytics"},
		{"/dashboard/playground", "Playground"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.path, nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Errorf("status = %d, want 200", w.Code)
			}
			if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
				t.Errorf("Content-Type = %q, want text/html", ct)
			}
			if !strings.Contains(w.Body.String(), tt.title) {
				t.Errorf("page missing title %q", tt.title)
			}
		})
	}
}

func TestDashboardRedirect(t *testing.T) {
	ks := testKeyStore()
	r := newRouter(testRegistry(), ks, nil, nil, nil, nil, nil, nil, "")
	req := httptest.NewRequest("GET", "/dashboard", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/dashboard/getting-started" {
		t.Errorf("Location = %q, want /dashboard/getting-started", loc)
	}
}

func TestDashboardStaticAssets(t *testing.T) {
	ks := testKeyStore()
	r := newRouter(testRegistry(), ks, nil, nil, nil, nil, nil, nil, "")

	assets := []string{
		"/dashboard/static/style.css",
		"/dashboard/static/dashboard.js",
		"/dashboard/static/pages/overview.js",
		"/dashboard/static/pages/keys.js",
	}
	for _, path := range assets {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest("GET", path, nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Errorf("status = %d, want 200", w.Code)
			}
		})
	}
}

func TestChatCompletions(t *testing.T) {
	t.Setenv("ALLOW_UNAUTHENTICATED_PROXY", "true")
	ks := testKeyStore()
	r := newRouter(testRegistry(), ks, nil, nil, nil, nil, nil, nil, "")
	payload := `{"model":"test-model","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(payload))
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
	req, err := decodeChatCompletionRequest(strings.NewReader(`{
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
		t.Fatalf("decodeChatCompletionRequest: %v", err)
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
	req, err := decodeChatCompletionRequest(strings.NewReader(`{
		"model":"test-model",
		"messages":[{"role":"user","content":"hi"}],
		"tool_choice":"auto"
	}`))
	if err != nil {
		t.Fatalf("decodeChatCompletionRequest: %v", err)
	}
	if req.ToolChoice != "auto" {
		t.Fatalf("tool_choice = %#v, want %q", req.ToolChoice, "auto")
	}
}

func TestChatCompletions_ValidationError(t *testing.T) {
	t.Setenv("ALLOW_UNAUTHENTICATED_PROXY", "true")
	ks := testKeyStore()
	r := newRouter(testRegistry(), ks, nil, nil, nil, nil, nil, nil, "")
	payload := `{"model":"","messages":[]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(payload))
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
	r := newRouter(testRegistry(), ks, nil, nil, nil, nil, nil, nil, "")
	payload := `{"model":"unknown","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
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
	r := newRouter(testStreamRegistry(), ks, nil, nil, nil, nil, nil, nil, "")
	payload := `{"model":"test-stream-model","messages":[{"role":"user","content":"hi"}],"stream":true}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(payload))
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
	writeSSE(context.Background(), w, ch)

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
	r := newRouter(testRegistry(), ks, nil, nil, nil, nil, nil, nil, "")
	payload := `{"model":"test-model","messages":[{"role":"user","content":"hi"}],"stream":true}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestCreateKeyStoreFromEnv_DefaultMemory(t *testing.T) {
	t.Setenv("API_KEY_STORE_BACKEND", "")
	t.Setenv("API_KEY_STORE_DSN", "")

	store, backend, err := createKeyStoreFromEnv()
	if err != nil {
		t.Fatalf("createKeyStoreFromEnv returned error: %v", err)
	}
	if backend != "memory" {
		t.Fatalf("backend = %s, want memory", backend)
	}
	if _, ok := store.(*admin.KeyStore); !ok {
		t.Fatalf("expected memory KeyStore type")
	}
}

func BenchmarkWriteSSE(b *testing.B) {
	chunk := providers.StreamChunk{
		ID:    "stream-1",
		Model: "test-stream-model",
		Choices: []providers.StreamChoice{{
			Index: 0,
			Delta: providers.MessageDelta{Role: "assistant", Content: "hello"},
		}},
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ch := make(chan providers.StreamChunk, 1)
		ch <- chunk
		close(ch)

		w := httptest.NewRecorder()
		writeSSE(context.Background(), w, ch)
	}
}

func BenchmarkDecodeChatCompletionRequest(b *testing.B) {
	payload := []byte(`{
		"model":"test-model",
		"messages":[
			{"role":"system","content":"You are a helpful assistant."},
			{"role":"user","content":"Summarize the latest deploy and highlight errors."},
			{"role":"user","content":[
				{"type":"text","text":"Also mention latency regressions."}
			]}
		],
		"temperature":0.2,
		"max_tokens":512,
		"stream":false,
		"tools":[
			{
				"type":"function",
				"function":{
					"name":"get_release_status",
					"description":"Fetch release status",
					"parameters":{
						"type":"object",
						"properties":{"environment":{"type":"string"}},
						"required":["environment"]
					}
				}
			}
		],
		"metadata":{"tenant":"bench","request_id":"req-123"}
	}`)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := decodeChatCompletionRequest(bytes.NewReader(payload)); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEncodeChatCompletionResponse(b *testing.B) {
	resp := providers.Response{
		ID:       "chatcmpl-bench",
		Object:   "chat.completion",
		Created:  1710000000,
		Model:    "test-model",
		Provider: "test",
		Choices: []providers.Choice{{
			Index: 0,
			Message: providers.Message{
				Role:    "assistant",
				Content: "Deployment completed successfully. One provider showed elevated p95 latency, but error rate remained stable.",
			},
			FinishReason: "stop",
		}},
		Usage: providers.Usage{
			PromptTokens:     128,
			CompletionTokens: 32,
			TotalTokens:      160,
		},
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf := bytes.NewBuffer(make([]byte, 0, 512))
		if err := json.NewEncoder(buf).Encode(resp); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkWriteOpenAIError(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		writeOpenAIError(w, http.StatusBadRequest, "invalid request", "invalid_request_error", "invalid_request")
		_, _ = io.Copy(io.Discard, w.Result().Body)
	}
}

func TestCreateKeyStoreFromEnv_SQLite(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "keys.db")
	t.Setenv("API_KEY_STORE_BACKEND", "sqlite")
	t.Setenv("API_KEY_STORE_DSN", dsn)

	store, backend, err := createKeyStoreFromEnv()
	if err != nil {
		t.Fatalf("createKeyStoreFromEnv returned error: %v", err)
	}
	if backend != "sqlite" {
		t.Fatalf("backend = %s, want sqlite", backend)
	}

	created, err := store.Create("test", nil, nil)
	if err != nil {
		t.Fatalf("create key on sqlite store: %v", err)
	}
	if _, ok := store.ValidateKey(created.Key); !ok {
		t.Fatalf("expected created sqlite key to validate")
	}
}

func TestCreateKeyStoreFromEnv_UnknownBackend(t *testing.T) {
	t.Setenv("API_KEY_STORE_BACKEND", "unknown")
	t.Setenv("API_KEY_STORE_DSN", "")

	if _, _, err := createKeyStoreFromEnv(); err == nil {
		t.Fatalf("expected error for unsupported backend")
	}
}

func TestCreateKeyStoreFromEnv_PostgresMissingDSN(t *testing.T) {
	t.Setenv("API_KEY_STORE_BACKEND", "postgres")
	t.Setenv("API_KEY_STORE_DSN", "")

	if _, _, err := createKeyStoreFromEnv(); err == nil {
		t.Fatalf("expected error for missing postgres dsn")
	}
}

func TestCreateConfigManagerFromEnv_DefaultMemory(t *testing.T) {
	t.Setenv("CONFIG_STORE_BACKEND", "")
	t.Setenv("CONFIG_STORE_DSN", "")

	gw := newTestGateway(t, aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
		Targets:  []aigateway.Target{{VirtualKey: "openai"}},
	})

	mgr, backend, err := createConfigManagerFromEnv(gw)
	if err != nil {
		t.Fatalf("createConfigManagerFromEnv returned error: %v", err)
	}
	if backend != "memory" {
		t.Fatalf("backend = %s, want memory", backend)
	}
	if cfg := mgr.GetConfig(); cfg.Strategy.Mode != aigateway.ModeSingle {
		t.Fatalf("unexpected config mode: %s", cfg.Strategy.Mode)
	}
}

func TestCreateConfigManagerFromEnv_SQLitePersistence(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "config.db")
	t.Setenv("CONFIG_STORE_BACKEND", "sqlite")
	t.Setenv("CONFIG_STORE_DSN", dsn)

	initialCfg := aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
		Targets:  []aigateway.Target{{VirtualKey: "openai"}},
	}
	updatedCfg := aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeFallback},
		Targets: []aigateway.Target{
			{VirtualKey: "openai"},
			{VirtualKey: "anthropic"},
		},
	}

	gw1 := newTestGateway(t, initialCfg)
	mgr1, backend, err := createConfigManagerFromEnv(gw1)
	if err != nil {
		t.Fatalf("createConfigManagerFromEnv returned error: %v", err)
	}
	if backend != "sqlite" {
		t.Fatalf("backend = %s, want sqlite", backend)
	}
	if err := mgr1.ReloadConfig(updatedCfg); err != nil {
		t.Fatalf("reload config via manager: %v", err)
	}

	gw2 := newTestGateway(t, initialCfg)
	mgr2, _, err := createConfigManagerFromEnv(gw2)
	if err != nil {
		t.Fatalf("createConfigManagerFromEnv (second) returned error: %v", err)
	}
	loaded := mgr2.GetConfig()
	if loaded.Strategy.Mode != aigateway.ModeFallback {
		t.Fatalf("expected persisted fallback mode, got %s", loaded.Strategy.Mode)
	}
	if len(loaded.Targets) != 2 {
		t.Fatalf("expected persisted 2 targets, got %d", len(loaded.Targets))
	}
}

func TestCreateConfigManagerFromEnv_UnknownBackend(t *testing.T) {
	t.Setenv("CONFIG_STORE_BACKEND", "unknown")
	t.Setenv("CONFIG_STORE_DSN", "")

	gw := newTestGateway(t, aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
		Targets:  []aigateway.Target{{VirtualKey: "openai"}},
	})

	if _, _, err := createConfigManagerFromEnv(gw); err == nil {
		t.Fatalf("expected error for unsupported backend")
	}
}

func TestCreateConfigManagerFromEnv_PostgresMissingDSN(t *testing.T) {
	t.Setenv("CONFIG_STORE_BACKEND", "postgres")
	t.Setenv("CONFIG_STORE_DSN", "")

	gw := newTestGateway(t, aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
		Targets:  []aigateway.Target{{VirtualKey: "openai"}},
	})

	if _, _, err := createConfigManagerFromEnv(gw); err == nil {
		t.Fatalf("expected error for missing postgres dsn")
	}
}

func TestNewHTTPServer_SetsHardeningTimeouts(t *testing.T) {
	srv := newHTTPServer(":8080", http.NewServeMux())
	if srv.ReadTimeout != serverReadTimeout {
		t.Fatalf("ReadTimeout = %v, want %v", srv.ReadTimeout, serverReadTimeout)
	}
	if srv.ReadHeaderTimeout != serverReadHeaderTimeout {
		t.Fatalf("ReadHeaderTimeout = %v, want %v", srv.ReadHeaderTimeout, serverReadHeaderTimeout)
	}
	if srv.WriteTimeout != serverWriteTimeout {
		t.Fatalf("WriteTimeout = %v, want %v", srv.WriteTimeout, serverWriteTimeout)
	}
	if srv.IdleTimeout != serverIdleTimeout {
		t.Fatalf("IdleTimeout = %v, want %v", srv.IdleTimeout, serverIdleTimeout)
	}
	if srv.MaxHeaderBytes != serverMaxHeaderBytes {
		t.Fatalf("MaxHeaderBytes = %d, want %d", srv.MaxHeaderBytes, serverMaxHeaderBytes)
	}
}

func TestCloseResources_AggregatesCloserErrors(t *testing.T) {
	err := closeResources(
		namedResource{name: "first", value: testCloser{err: errors.New("boom one")}},
		namedResource{name: "second", value: testCloser{err: errors.New("boom two")}},
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
	status, errType, code := routeErrorDetails(&plugin.RejectionError{Stage: plugin.StageBeforeRequest, Reason: "blocked"})
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
	status, errType, code := routeErrorDetails(err)
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
	status, errType, code := routeErrorDetails(&plugin.RejectionError{Stage: plugin.StageAfterRequest, Reason: "schema mismatch"})
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
	status, errType, code := routeErrorDetails(&plugin.RejectionError{Stage: plugin.Stage("custom_stage"), Reason: "custom"})
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
	status, errType, code := routeErrorDetails(errors.New("boom"))
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

func newTestGateway(t *testing.T, cfg aigateway.Config) *aigateway.Gateway {
	t.Helper()
	gw, err := aigateway.New(cfg)
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	return gw
}

type testCloser struct {
	err error
}

func (c testCloser) Close() error {
	return c.err
}
