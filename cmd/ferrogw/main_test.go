package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/internal/admin"
	"github.com/ferro-labs/ai-gateway/providers"
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
	r := newRouter(testRegistry(), ks, nil, nil, nil, nil, nil, nil)
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
	ks := testKeyStore()
	r := newRouter(testRegistry(), ks, nil, nil, nil, nil, nil, nil)
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

func TestDashboardUIPage(t *testing.T) {
	ks := testKeyStore()
	r := newRouter(testRegistry(), ks, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest("GET", "/dashboard", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	if !strings.Contains(w.Body.String(), "FerroGateway Dashboard") {
		t.Errorf("dashboard html missing title")
	}
	if !strings.Contains(w.Body.String(), "/admin/config/history") {
		t.Errorf("dashboard html missing config history integration")
	}
	if !strings.Contains(w.Body.String(), "/admin/config/rollback/") {
		t.Errorf("dashboard html missing rollback integration")
	}
	if !strings.Contains(w.Body.String(), "window.confirm(") {
		t.Errorf("dashboard html missing rollback confirmation safeguard")
	}
}

func TestChatCompletions(t *testing.T) {
	ks := testKeyStore()
	r := newRouter(testRegistry(), ks, nil, nil, nil, nil, nil, nil)
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

func TestChatCompletions_ValidationError(t *testing.T) {
	ks := testKeyStore()
	r := newRouter(testRegistry(), ks, nil, nil, nil, nil, nil, nil)
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
	ks := testKeyStore()
	r := newRouter(testRegistry(), ks, nil, nil, nil, nil, nil, nil)
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
	ks := testKeyStore()
	r := newRouter(testStreamRegistry(), ks, nil, nil, nil, nil, nil, nil)
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

func TestChatCompletions_StreamUnsupported(t *testing.T) {
	ks := testKeyStore()
	r := newRouter(testRegistry(), ks, nil, nil, nil, nil, nil, nil)
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

func newTestGateway(t *testing.T, cfg aigateway.Config) *aigateway.Gateway {
	t.Helper()
	gw, err := aigateway.New(cfg)
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	return gw
}
