package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/internal/admin/model"
	"github.com/ferro-labs/ai-gateway/internal/admin/repository"
	"github.com/ferro-labs/ai-gateway/internal/requestlog"
	"github.com/go-chi/chi/v5"
)

type testConfigManager struct {
	cfg     config.Config
	initial config.Config
}

type persistenceFailingConfigManager struct {
	cfg config.Config
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
		if len(query.Stages) > 0 && !slices.Contains(query.Stages, entry.Stage) {
			continue
		}
		if query.Model != "" && entry.Model != query.Model {
			continue
		}
		if query.Provider != "" && entry.Provider != query.Provider {
			continue
		}
		// Matched on the pointer's presence, not on emptiness: a pointer to ""
		// is the request for the rows naming no credential, which the store
		// answers with `api_key_id IS NULL OR api_key_id = ''`.
		if query.APIKeyID != nil && entry.APIKeyID != *query.APIKeyID {
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
		ByStage:    map[string]requestlog.DimensionStat{},
		ByProvider: map[string]requestlog.DimensionStat{},
		ByModel:    map[string]requestlog.DimensionStat{},
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

func (m *testConfigManager) GetConfig() config.Config {
	return m.cfg
}

func (m *testConfigManager) ReloadConfig(_ context.Context, cfg config.Config) error {
	if err := config.ValidateConfig(cfg); err != nil {
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

func (m *persistenceFailingConfigManager) GetConfig() config.Config {
	return m.cfg
}

func (m *persistenceFailingConfigManager) ReloadConfig(_ context.Context, _ config.Config) error {
	return fmt.Errorf("%w: write failed", repository.ErrConfigPersistence)
}

func (m *persistenceFailingConfigManager) Ping(_ context.Context) error {
	return nil
}

func setupTestRouterWithConfigManager(cm ConfigManager) (*Handlers, chi.Router) {
	store := repository.NewKeyStore()
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
	store := repository.NewKeyStore()
	cm := &testConfigManager{
		cfg: config.Config{
			Strategy: config.StrategyConfig{Mode: config.ModeSingle},
			Targets:  []config.Target{{VirtualKey: "openai"}},
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
	store := repository.NewKeyStore()
	cm := &testConfigManager{
		cfg: config.Config{
			Strategy: config.StrategyConfig{Mode: config.ModeSingle},
			Targets:  []config.Target{{VirtualKey: "openai"}},
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

// routerWithStores builds the same router shape as setupTestRouterWithSessions
// over caller-supplied Store/SessionStore/masterKey, so a test can swap in a
// SQL-backed SessionStore, or a non-empty masterKey, while keeping the exact
// route wiring production goes through.
func routerWithStores(keys repository.Store, sessions repository.SessionStore, masterKey string) (*Handlers, chi.Router) {
	cm := &testConfigManager{
		cfg: config.Config{
			Strategy: config.StrategyConfig{Mode: config.ModeSingle},
			Targets:  []config.Target{{VirtualKey: "openai"}},
		},
	}
	cm.initial = cm.cfg
	validate, _ := NewCredentialValidator(keys, masterKey)
	h := &Handlers{
		Keys:        keys,
		Configs:     cm,
		Sessions:    sessions,
		Credentials: validate,
	}
	r := chi.NewRouter()
	// Unauthenticated, exactly as router.go mounts it: this route is how a
	// caller obtains the credential the middleware checks.
	r.Post("/admin/session", h.CreateSessionHandler())
	r.Group(func(r chi.Router) {
		r.Use(AuthMiddlewareWithSessions(keys, sessions, masterKey))
		r.Mount("/admin", h.Routes())
	})
	return h, r
}

// setupTestRouterWithSessions mirrors setupTestRouter but wires the session
// store into the middleware and mounts the unauthenticated exchange route the
// way the real router does.
func setupTestRouterWithSessions() (*Handlers, chi.Router) {
	return routerWithStores(repository.NewKeyStore(), repository.NewSessionStore(), "")
}

// tokenRequest builds an authenticated request from a raw bearer token.
// authedRequest takes an *APIKey and cannot carry a session token.
//
// Uses NewRequestWithContext rather than plain NewRequest to satisfy the same
// noctx lint rule authedRequest already follows.
func tokenRequest(method, url, body, token string) *http.Request {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequestWithContext(context.Background(), method, url, reader)
	req.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

func createAdminKey(t *testing.T, h *Handlers) *model.APIKey {
	t.Helper()
	return createTestKey(t, h, "admin-key", []string{model.ScopeAdmin}, nil)
}

func createTestKey(t *testing.T, h *Handlers, name string, scopes []string, expiresAt *time.Time) *model.APIKey {
	t.Helper()
	key, err := h.Keys.Create(t.Context(), name, scopes, expiresAt)
	if err != nil {
		t.Fatalf("create test key %q: %v", name, err)
	}
	return key
}

func createReadOnlyKey(t *testing.T, h *Handlers) *model.APIKey {
	t.Helper()
	return createTestKey(t, h, "readonly-key", []string{model.ScopeReadOnly}, nil)
}

func validateTestKey(t *testing.T, h *Handlers, key string) *model.APIKey {
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
func authedRequest(method, url string, body string, apiKey *model.APIKey) *http.Request {
	var req *http.Request
	if body != "" {
		req = httptest.NewRequestWithContext(context.Background(), method, url, bytes.NewBufferString(body))
	} else {
		req = httptest.NewRequestWithContext(context.Background(), method, url, nil)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey.Key)
	return req
}
