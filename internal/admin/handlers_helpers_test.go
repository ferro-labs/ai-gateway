package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/internal/requestlog"
	"github.com/go-chi/chi/v5"
)

type testConfigManager struct {
	cfg     aigateway.Config
	initial aigateway.Config
}

type persistenceFailingConfigManager struct {
	cfg aigateway.Config
}

const fallbackConfigBody = `{"strategy":{"mode":"fallback"},"targets":[{"virtual_key":"openai"},{"virtual_key":"anthropic"}]}`

type fakeLogReader struct {
	entries []requestlog.Entry
	stats   requestlog.StatsResult
}

func (f *fakeLogReader) Stats(_ context.Context, _ requestlog.Query) (requestlog.StatsResult, error) {
	return f.stats, nil
}

func (f *fakeLogReader) List(_ context.Context, query requestlog.Query) (requestlog.ListResult, error) {
	filtered := make([]requestlog.Entry, 0)
	for _, entry := range f.entries {
		if query.Stage != "" && entry.Stage != query.Stage {
			continue
		}
		if query.Model != "" && entry.Model != query.Model {
			continue
		}
		if query.Provider != "" && entry.Provider != query.Provider {
			continue
		}
		if query.Since != nil && entry.CreatedAt.Before(*query.Since) {
			continue
		}
		filtered = append(filtered, entry)
	}

	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].CreatedAt.After(filtered[j].CreatedAt)
	})

	start := query.Offset
	if start > len(filtered) {
		start = len(filtered)
	}
	end := start + query.Limit
	if query.Limit <= 0 || end > len(filtered) {
		end = len(filtered)
	}

	return requestlog.ListResult{Data: filtered[start:end], Total: len(filtered)}, nil
}

type fakeLogStore struct {
	entries []requestlog.Entry
}

func (f *fakeLogStore) List(_ context.Context, query requestlog.Query) (requestlog.ListResult, error) {
	reader := &fakeLogReader{entries: f.entries}
	return reader.List(context.Background(), query)
}

func (f *fakeLogStore) Stats(_ context.Context, _ requestlog.Query) (requestlog.StatsResult, error) {
	return requestlog.StatsResult{
		ByStage:    map[string]int{},
		ByProvider: map[string]int{},
		ByModel:    map[string]int{},
	}, nil
}

func (f *fakeLogStore) Delete(_ context.Context, query requestlog.MaintenanceQuery) (int, error) {
	if query.Before == nil {
		return 0, nil
	}

	remaining := make([]requestlog.Entry, 0, len(f.entries))
	deleted := 0
	for _, entry := range f.entries {
		if !entry.CreatedAt.Before(*query.Before) {
			remaining = append(remaining, entry)
			continue
		}
		if query.Stage != "" && entry.Stage != query.Stage {
			remaining = append(remaining, entry)
			continue
		}
		if query.Model != "" && entry.Model != query.Model {
			remaining = append(remaining, entry)
			continue
		}
		if query.Provider != "" && entry.Provider != query.Provider {
			remaining = append(remaining, entry)
			continue
		}
		deleted++
	}

	f.entries = remaining
	return deleted, nil
}

func (m *testConfigManager) GetConfig() aigateway.Config {
	return m.cfg
}

func (m *testConfigManager) ReloadConfig(_ context.Context, cfg aigateway.Config) error {
	if err := aigateway.ValidateConfig(cfg); err != nil {
		return err
	}
	m.cfg = cfg
	return nil
}

func (m *testConfigManager) ResetConfig(_ context.Context) error {
	m.cfg = m.initial
	return nil
}

func (m *testConfigManager) Ping(_ context.Context) error {
	return nil
}

func (m *persistenceFailingConfigManager) GetConfig() aigateway.Config {
	return m.cfg
}

func (m *persistenceFailingConfigManager) ReloadConfig(_ context.Context, _ aigateway.Config) error {
	return fmt.Errorf("%w: write failed", errConfigPersistence)
}

func (m *persistenceFailingConfigManager) Ping(_ context.Context) error {
	return nil
}

func setupTestRouterWithConfigManager(cm ConfigManager) (*Handlers, chi.Router) {
	store := NewKeyStore()
	h := &Handlers{
		Keys:    store,
		Configs: cm,
	}
	r := chi.NewRouter()
	r.Use(AuthMiddleware(store, ""))
	r.Mount("/admin", h.Routes())
	return h, r
}

func setupTestRouter() (*Handlers, chi.Router) {
	store := NewKeyStore()
	cm := &testConfigManager{
		cfg: aigateway.Config{
			Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
			Targets:  []aigateway.Target{{VirtualKey: "openai"}},
		},
	}
	cm.initial = cm.cfg
	h := &Handlers{
		Keys:    store,
		Configs: cm,
	}
	r := chi.NewRouter()
	r.Use(AuthMiddleware(store, ""))
	r.Mount("/admin", h.Routes())
	return h, r
}

func setupTestRouterWithLogs(reader requestlog.Reader) (*Handlers, chi.Router) {
	store := NewKeyStore()
	cm := &testConfigManager{
		cfg: aigateway.Config{
			Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
			Targets:  []aigateway.Target{{VirtualKey: "openai"}},
		},
	}
	cm.initial = cm.cfg
	h := &Handlers{
		Keys:    store,
		Configs: cm,
		Logs:    reader,
	}
	if maintainer, ok := reader.(requestlog.Maintainer); ok {
		h.LogAdmin = maintainer
	}
	r := chi.NewRouter()
	r.Use(AuthMiddleware(store, ""))
	r.Mount("/admin", h.Routes())
	return h, r
}

func createAdminKey(t *testing.T, h *Handlers) *APIKey {
	t.Helper()
	return createTestKey(t, h, "admin-key", []string{ScopeAdmin}, nil)
}

func createTestKey(t *testing.T, h *Handlers, name string, scopes []string, expiresAt *time.Time) *APIKey {
	t.Helper()
	key, err := h.Keys.Create(t.Context(), name, scopes, expiresAt)
	if err != nil {
		t.Fatalf("create test key %q: %v", name, err)
	}
	return key
}

func createReadOnlyKey(t *testing.T, h *Handlers) *APIKey {
	t.Helper()
	return createTestKey(t, h, "readonly-key", []string{ScopeReadOnly}, nil)
}

func validateTestKey(t *testing.T, h *Handlers, key string) *APIKey {
	t.Helper()
	validated, ok := h.Keys.ValidateKey(t.Context(), key)
	if !ok {
		t.Fatal("validate test key: key was rejected")
	}
	return validated
}

func decodeJSON(t testing.TB, r io.Reader, dst any) {
	t.Helper()
	if err := json.NewDecoder(r).Decode(dst); err != nil {
		t.Fatalf("decode JSON response: %v", err)
	}
}

// No *testing.T is available in this standalone builder (74 call sites across
// this file; threading t through all of them buys nothing since these tests
// exercise handler auth/routing, not context cancellation).
func authedRequest(method, url string, body string, apiKey *APIKey) *http.Request {
	var req *http.Request
	if body != "" {
		req = httptest.NewRequestWithContext(context.Background(), method, url, bytes.NewBufferString(body))
	} else {
		req = httptest.NewRequestWithContext(context.Background(), method, url, nil)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey.Key)
	return req
}
