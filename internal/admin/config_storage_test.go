package admin

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/go-chi/chi/v5"
)

func configWithLogger(config map[string]any) aigateway.Config {
	return aigateway.Config{
		Plugins: []aigateway.PluginConfig{
			{Name: "request-logger", Enabled: true, Config: config},
		},
	}
}

// A submitted config may not point a plugin at a different database or file.
// requestlog.NewSQLiteWriter creates the file it is given and restricts its
// permissions, so a request-supplied dsn is an arbitrary file create and chmod.
func TestResolveStorageOptionsRejectsRedirectedStorage(t *testing.T) {
	running := configWithLogger(map[string]any{"persist": true, "dsn": "/var/lib/ferrogw/requests.db"})

	for _, tc := range []struct {
		name      string
		submitted aigateway.Config
	}{
		{
			name:      "absolute path elsewhere",
			submitted: configWithLogger(map[string]any{"persist": true, "dsn": "/etc/cron.d/payload"}),
		},
		{
			name:      "traversal",
			submitted: configWithLogger(map[string]any{"persist": true, "dsn": "../../../etc/shadow"}),
		},
		{
			name:      "cleared so the writer falls back to a new default path",
			submitted: configWithLogger(map[string]any{"persist": true}),
		},
		{
			name:      "redirected backend",
			submitted: configWithLogger(map[string]any{"persist": true, "dsn": "/var/lib/ferrogw/requests.db", "backend": "postgres"}),
		},
		{
			name: "plugin introduced with storage the process never had",
			submitted: aigateway.Config{Plugins: []aigateway.PluginConfig{
				{Name: "request-logger", Enabled: true, Config: map[string]any{"persist": true, "dsn": "/var/lib/ferrogw/requests.db"}},
				{Name: "second-logger", Enabled: true, Config: map[string]any{"persist": true, "dsn": "/tmp/attacker.db"}},
			}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := resolveStorageOptions(running, tc.submitted); !errors.Is(err, ErrStorageOptionChanged) {
				t.Fatalf("resolveStorageOptions accepted a redirected store: err = %v", err)
			}
		})
	}
}

// A disabled plugin is never constructed, but its storage option must still be
// pinned: otherwise it could be set while disabled and take effect on the next
// request that enables it.
func TestResolveStorageOptionsGuardsDisabledPlugins(t *testing.T) {
	running := aigateway.Config{Plugins: []aigateway.PluginConfig{
		{Name: "request-logger", Enabled: false, Config: map[string]any{"dsn": "/var/lib/ferrogw/requests.db"}},
	}}
	submitted := aigateway.Config{Plugins: []aigateway.PluginConfig{
		{Name: "request-logger", Enabled: false, Config: map[string]any{"dsn": "/tmp/attacker.db"}},
	}}

	if _, err := resolveStorageOptions(running, submitted); !errors.Is(err, ErrStorageOptionChanged) {
		t.Fatalf("a disabled plugin's storage option was not guarded: err = %v", err)
	}
}

// Everything else about a plugin stays editable, and the resolved storage value
// comes from the running config rather than the request.
func TestResolveStorageOptionsPreservesTheRestOfTheConfig(t *testing.T) {
	const dsn = "/var/lib/ferrogw/requests.db"
	running := configWithLogger(map[string]any{"persist": true, "dsn": dsn, "level": "info"})
	submitted := configWithLogger(map[string]any{"persist": true, "dsn": dsn, "level": "debug"})

	resolved, err := resolveStorageOptions(running, submitted)
	if err != nil {
		t.Fatalf("resolveStorageOptions: %v", err)
	}
	got := resolved.Plugins[0].Config
	if got["level"] != "debug" {
		t.Fatalf("level = %v, want debug: unrelated options must stay editable", got["level"])
	}
	if got["dsn"] != dsn {
		t.Fatalf("dsn = %v, want %q", got["dsn"], dsn)
	}

	// The caller's map must not be rewritten in place.
	if _, mutated := submitted.Plugins[0].Config["backend"]; mutated {
		t.Fatal("resolveStorageOptions mutated the submitted config")
	}
}

// Adding a plugin that names no storage is allowed: nothing reaches the
// filesystem.
func TestResolveStorageOptionsAllowsPluginsWithoutStorage(t *testing.T) {
	running := aigateway.Config{}
	submitted := aigateway.Config{Plugins: []aigateway.PluginConfig{
		{Name: "word-filter", Enabled: true, Config: map[string]any{"blocked_words": []string{"secret"}}},
		{Name: "request-logger", Enabled: true, Config: map[string]any{"persist": false, "level": "warn"}},
	}}

	resolved, err := resolveStorageOptions(running, submitted)
	if err != nil {
		t.Fatalf("resolveStorageOptions rejected a config that names no storage: %v", err)
	}
	if _, present := resolved.Plugins[1].Config["dsn"]; present {
		t.Fatal("an absent dsn was materialized into the resolved config")
	}
}

// A non-string value names no storage location and must not be treated as one.
func TestStringOptionIgnoresNonStrings(t *testing.T) {
	if got := stringOption(map[string]any{"dsn": 42}, "dsn"); got != "" {
		t.Fatalf("stringOption(42) = %q, want empty", got)
	}
	if got := stringOption(nil, "dsn"); got != "" {
		t.Fatalf("stringOption(nil) = %q, want empty", got)
	}
}

// The end-to-end path CodeQL reported: a request body reaching
// requestlog.NewSQLiteWriter, which creates a file at the path it is given.
func TestUpdateConfigRejectsRedirectedPluginStorage(t *testing.T) {
	store := NewKeyStore()
	cm := &testConfigManager{
		cfg: aigateway.Config{
			Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
			Targets:  []aigateway.Target{{VirtualKey: "openai"}},
			Plugins: []aigateway.PluginConfig{
				{Name: "request-logger", Type: "logging", Stage: "after_request", Enabled: true,
					Config: map[string]any{"persist": true, "dsn": "/var/lib/ferrogw/requests.db"}},
			},
		},
	}
	cm.initial = cm.cfg
	h := &Handlers{Keys: store, Configs: cm}
	r := chi.NewRouter()
	r.Use(AuthMiddleware(store, ""))
	r.Mount("/admin", h.Routes())

	adminKey := createAdminKey(t, h)
	body := `{"strategy":{"mode":"single"},"targets":[{"virtual_key":"openai"}],` +
		`"plugins":[{"name":"request-logger","type":"logging","stage":"after_request","enabled":true,` +
		`"config":{"persist":true,"dsn":"/tmp/ferrogw-attacker.db"}}]}`

	req := authedRequest(http.MethodPut, "/admin/config", body, adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: the admin API redirected the request log to %q", w.Code, "/tmp/ferrogw-attacker.db")
	}
	if got := cm.GetConfig().Plugins[0].Config["dsn"]; got != "/var/lib/ferrogw/requests.db" {
		t.Fatalf("running dsn = %v; the rejected config was applied anyway", got)
	}
}
