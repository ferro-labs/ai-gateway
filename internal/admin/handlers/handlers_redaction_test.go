package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/internal/admin/model"
	"github.com/ferro-labs/ai-gateway/internal/admin/repository"
	"github.com/ferro-labs/ai-gateway/mcp"
	"github.com/go-chi/chi/v5"
)

func TestGetConfigRedactsSecrets(t *testing.T) {
	store := repository.NewKeyStore()

	cfg := config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "openai"}},
		MCPServers: []mcp.ServerConfig{
			{
				Name: "tools",
				URL:  "https://mcp.example.com/mcp",
				Headers: map[string]string{
					"Authorization": "literal-mcp-secret",
					"X-Env":         "${MCP_TOKEN}",
				},
			},
			{
				Name:    "stdio-tools",
				Command: "some-mcp-server",
				Env: map[string]string{
					"API_KEY":  "literal-mcp-env-secret",
					"REF_ONLY": "${MCP_ENV_TOKEN}",
				},
			},
		},
		Observability: config.ObservabilityConfig{
			Tracing: config.TracingConfig{
				Headers: map[string]string{
					"Authorization": "literal-secret-value",
					"X-Api-Key":     "${MY_API_KEY}",
				},
			},
			Exporters: []config.ExporterConfig{
				{
					Name:    "langsmith",
					Enabled: true,
					Config: map[string]any{
						"api_key": "literal-ls-key",
						"debug":   true,
					},
				},
			},
		},
		Plugins: []config.PluginConfig{
			{
				Name:    "word-filter",
				Type:    "guardrail",
				Stage:   "before_request",
				Enabled: true,
				Config: map[string]any{
					"secret":        "literal-plugin-secret",
					"blocked_words": []any{"password"},
				},
			},
		},
	}

	cm := &testConfigManager{cfg: cfg, initial: cfg}
	h := &Handlers{Keys: store, Configs: cm}
	r := chi.NewRouter()
	r.Use(AuthMiddleware(store, ""))
	r.Mount("/admin", h.Routes())

	adminKey, err := store.Create(context.Background(), "admin-key", []string{model.ScopeAdmin}, nil)
	if err != nil {
		t.Fatalf("create admin key: %v", err)
	}

	req := authedRequest(http.MethodGet, "/admin/config", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var respCfg config.Config
	if err := json.NewDecoder(w.Body).Decode(&respCfg); err != nil {
		t.Fatalf("decode config response: %v", err)
	}

	// Every one of these maps is withheld whole: the KEY is replaced along with
	// the value, so nothing is read back by the name the operator wrote. What is
	// asserted instead is that the name is gone, the literal is gone, and the
	// entry count survives so an operator can still see settings exist.
	withheld := map[string]struct {
		entries  map[string]any
		names    []string
		shown    []string
		literals []string
		refs     []string
	}{
		"observability.tracing.headers": {
			entries:  toAny(respCfg.Observability.Tracing.Headers),
			names:    []string{"Authorization", "X-Api-Key"},
			literals: []string{"Bearer super-secret-token"},
			refs:     []string{"${MY_API_KEY}"},
		},
		"mcp_servers[0].headers": {
			entries:  toAny(respCfg.MCPServers[0].Headers),
			names:    []string{"Authorization", "X-Env"},
			literals: []string{"Bearer mcp-secret"},
			refs:     []string{"${MCP_TOKEN}"},
		},
		"mcp_servers[1].env": {
			entries:  toAny(respCfg.MCPServers[1].Env),
			names:    []string{"API_KEY", "REF_ONLY"},
			literals: []string{"literal-mcp-env-secret"},
			refs:     []string{"${MCP_ENV_TOKEN}"},
		},
		"observability.exporters[0].config": {
			entries: respCfg.Observability.Exporters[0].Config,
			names:   []string{"api_key", "debug"},
			// "debug" is a bool: a non-string value is withheld too, where it
			// used to be copied through untouched.
			literals: []string{"literal-ls-key"},
		},
		"plugins[0].config": {
			entries: respCfg.Plugins[0].Config,
			names:   []string{"secret"},
			// word-filter DECLARES blocked_words, so that key is a setting and
			// keeps its name and its shape. This is the half of the rule the
			// withholding must not break.
			shown:    []string{"blocked_words"},
			literals: []string{"literal-plugin-secret"},
		},
	}

	for path, want := range withheld {
		t.Run(path, func(t *testing.T) {
			if got, expect := len(want.entries), len(want.names)+len(want.shown); got != expect {
				t.Errorf("%d entries, want %d (%d withheld + %d shown)",
					got, expect, len(want.names), len(want.shown))
			}
			encoded, err := json.Marshal(want.entries)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			body := string(encoded)
			for _, name := range want.names {
				if strings.Contains(body, `"`+name+`"`) {
					t.Errorf("withheld key %q was served: %s", name, body)
				}
			}
			for _, name := range want.shown {
				if !strings.Contains(body, `"`+name+`"`) {
					t.Errorf("declared setting %q was withheld: %s", name, body)
				}
			}
			for _, literal := range want.literals {
				if strings.Contains(body, literal) {
					t.Errorf("literal survived: %s", body)
				}
			}
			// A ${VAR} reference names a value rather than carrying one, and the
			// inlined-literal warning keys off exactly this, so it survives.
			for _, ref := range want.refs {
				if !strings.Contains(body, ref) {
					t.Errorf("env reference %s was dropped: %s", ref, body)
				}
			}
		})
	}

	// Live config must NOT be mutated.
	liveCfg := cm.GetConfig()
	if got := liveCfg.Observability.Tracing.Headers["Authorization"]; got != "literal-secret-value" {
		t.Errorf("live config Authorization mutated: got %q, want literal-secret-value", got)
	}
	if got := liveCfg.MCPServers[0].Headers["Authorization"]; got != "literal-mcp-secret" {
		t.Errorf("live config MCP Authorization mutated: got %q, want literal-mcp-secret", got)
	}
	if got := liveCfg.MCPServers[1].Env["API_KEY"]; got != "literal-mcp-env-secret" {
		t.Errorf("live config MCP Env API_KEY mutated: got %q, want literal-mcp-env-secret", got)
	}
	if got := liveCfg.Observability.Exporters[0].Config["api_key"].(string); got != "literal-ls-key" {
		t.Errorf("live config api_key mutated: got %q, want literal-ls-key", got)
	}
	if got := liveCfg.Plugins[0].Config["secret"].(string); got != "literal-plugin-secret" {
		t.Errorf("live config plugin secret mutated: got %q, want literal-plugin-secret", got)
	}
}

func TestGetConfigPreservesEnvRefsAndNonStringValues(t *testing.T) {
	store := repository.NewKeyStore()

	// Build the env-ref string at runtime to avoid credential-scanner false positives
	// on the "${…}" literal form that gosec treats as a potential hardcoded credential.
	envRef := "${" + "PLUGIN_TOKEN" + "}"

	cfg := config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "openai"}},
		Observability: config.ObservabilityConfig{
			Exporters: []config.ExporterConfig{
				{
					Name:    "myplugin",
					Enabled: true,
					Config: map[string]any{
						"token":   envRef,
						"timeout": 30.0,
						"active":  true,
					},
				},
			},
		},
	}

	cm := &testConfigManager{cfg: cfg, initial: cfg}
	h := &Handlers{Keys: store, Configs: cm}
	r := chi.NewRouter()
	r.Use(AuthMiddleware(store, ""))
	r.Mount("/admin", h.Routes())

	adminKey, err := store.Create(context.Background(), "admin-key", []string{model.ScopeAdmin}, nil)
	if err != nil {
		t.Fatalf("create admin key: %v", err)
	}

	req := authedRequest(http.MethodGet, "/admin/config", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var respCfg config.Config
	if err := json.NewDecoder(w.Body).Decode(&respCfg); err != nil {
		t.Fatalf("decode config response: %v", err)
	}

	if len(respCfg.Observability.Exporters) == 0 {
		t.Fatal("expected exporters in response")
	}
	expCfg := respCfg.Observability.Exporters[0].Config

	// An exporter declares no schema, so every key here is withheld and none is
	// readable by the name the operator wrote.
	for _, name := range []string{"token", "timeout", "active"} {
		if _, ok := expCfg[name]; ok {
			t.Errorf("withheld key %q was served: %v", name, expCfg)
		}
	}
	if len(expCfg) != 3 {
		t.Errorf("%d entries, want 3 withheld placeholders", len(expCfg))
	}

	// The ${VAR} reference still survives as a VALUE. It names a value rather
	// than carrying one, and warnStoredLiterals decides whether a literal was
	// inlined by asking what scrubbing changed — replacing references here would
	// make that warning fire on every config that took its own advice.
	var refs int
	for _, v := range expCfg {
		if v == envRef {
			refs++
		}
	}
	if refs != 1 {
		t.Errorf("found %d env references, want the one written: %v", refs, expCfg)
	}

	// The non-string values do NOT survive. mapStrings copied them through
	// untouched, which is how a number under a withheld key was served.
	for _, v := range expCfg {
		switch v.(type) {
		case float64, bool:
			t.Errorf("a non-string value survived withholding: %v", expCfg)
		}
	}
}

// toAny lifts a map[string]string into the map[string]any shape the assertions
// above share, so one table can cover both.
func toAny(m map[string]string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
