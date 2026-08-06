package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/plugin"

	// The catalog describes what the binary ships, so the routes under test are
	// only meaningful with the built-ins registered, as cmd/ferrogw registers
	// them.
	_ "github.com/ferro-labs/ai-gateway/plugin/budget"
	_ "github.com/ferro-labs/ai-gateway/plugin/cache"
	_ "github.com/ferro-labs/ai-gateway/plugin/logger"
	_ "github.com/ferro-labs/ai-gateway/plugin/maxtoken"
	_ "github.com/ferro-labs/ai-gateway/plugin/ratelimit"
	_ "github.com/ferro-labs/ai-gateway/plugin/wordfilter"
)

type catalogPayload struct {
	Data []plugin.BuiltinPlugin `json:"data"`
}

func getCatalog(t *testing.T) (*httptest.ResponseRecorder, catalogPayload) {
	t.Helper()

	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, authedRequest(http.MethodGet, "/admin/plugins/catalog", "", adminKey))

	var payload catalogPayload
	if w.Code == http.StatusOK {
		if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
			t.Fatalf("decode catalog: %v", err)
		}
	}
	return w, payload
}

// TestPluginCatalogServesTheSettingsThePluginsRead is the contract that lets the
// dashboard stop carrying its own copy: the keys it renders come from the
// gateway, and plugin/catalog_test.go proves they are the keys the plugins read.
func TestPluginCatalogServesTheSettingsThePluginsRead(t *testing.T) {
	w, payload := getCatalog(t)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if len(payload.Data) != len(plugin.Builtins()) {
		t.Fatalf("expected %d entries, got %d", len(plugin.Builtins()), len(payload.Data))
	}

	byName := make(map[string]plugin.BuiltinPlugin, len(payload.Data))
	for _, entry := range payload.Data {
		byName[entry.Name] = entry
	}

	// The four the dashboard used to fabricate, named as the plugins read them.
	for name, want := range map[string]string{
		"rate-limit":     "requests_per_second",
		"budget":         "spend_limit_usd",
		"response-cache": "max_age",
		"request-logger": "level",
	} {
		entry, ok := byName[name]
		if !ok {
			t.Errorf("%s missing from the catalog", name)
			continue
		}
		found := false
		for _, setting := range entry.Settings {
			if setting == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: catalog does not offer %q, only %v", name, want, entry.Settings)
		}
	}
}

// TestPluginCatalogReportsTheFailurePolicy keeps the dashboard from deriving it
// from a type list of its own — the gateway reads the policy off the type the
// plugin reports, which is not the type the config document declares.
func TestPluginCatalogReportsTheFailurePolicy(t *testing.T) {
	_, payload := getCatalog(t)

	want := map[string]bool{
		"request-logger": true,  // logging: a dead log sink must not gate the request
		"rate-limit":     false, // ratelimit, though a config is free to call it a guardrail
		"budget":         false,
		"word-filter":    false,
		"max-token":      false,
		"response-cache": false,
	}
	for _, entry := range payload.Data {
		expected, known := want[entry.Name]
		if !known {
			continue
		}
		if entry.FailsOpen != expected {
			t.Errorf("%s: fails_open = %v, want %v", entry.Name, entry.FailsOpen, expected)
		}
	}
}

// TestPluginCatalogRequiresAuth keeps the new route inside the same fence as the
// admin routes around it.
func TestPluginCatalogRequiresAuth(t *testing.T) {
	_, r := setupTestRouter()

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/admin/plugins/catalog", nil))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without a credential, got %d", w.Code)
	}
}

// TestPluginCatalogDoesNotShadowConfiguredPlugins guards the route pair: chi
// matches on segments, and a catalog served at /admin/plugins would have
// replaced the list of what is actually configured.
func TestPluginCatalogDoesNotShadowConfiguredPlugins(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, authedRequest(http.MethodGet, "/admin/plugins", "", adminKey))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var configured []map[string]any
	if err := json.NewDecoder(w.Body).Decode(&configured); err != nil {
		t.Fatalf("GET /admin/plugins must still return the configured list: %v", err)
	}
}
