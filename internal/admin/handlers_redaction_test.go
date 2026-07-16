package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/mcp"
	"github.com/go-chi/chi/v5"
)

func TestGetConfigRedactsSecrets(t *testing.T) {
	store := NewKeyStore()

	cfg := aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
		Targets:  []aigateway.Target{{VirtualKey: "openai"}},
		MCPServers: []mcp.ServerConfig{
			{
				Name: "tools",
				URL:  "https://mcp.example.com/mcp",
				Headers: map[string]string{
					"Authorization": "literal-mcp-secret",
					"X-Env":         "${MCP_TOKEN}",
				},
			},
		},
		Observability: aigateway.ObservabilityConfig{
			Tracing: aigateway.TracingConfig{
				Headers: map[string]string{
					"Authorization": "literal-secret-value",
					"X-Api-Key":     "${MY_API_KEY}",
				},
			},
			Exporters: []aigateway.ExporterConfig{
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
		Plugins: []aigateway.PluginConfig{
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

	adminKey, err := store.Create(context.Background(), "admin-key", []string{ScopeAdmin}, nil)
	if err != nil {
		t.Fatalf("create admin key: %v", err)
	}

	req := authedRequest(http.MethodGet, "/admin/config", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var respCfg aigateway.Config
	if err := json.NewDecoder(w.Body).Decode(&respCfg); err != nil {
		t.Fatalf("decode config response: %v", err)
	}

	// Literal header value must be redacted.
	if got := respCfg.Observability.Tracing.Headers["Authorization"]; got != "[REDACTED]" {
		t.Errorf("Authorization header: got %q, want [REDACTED]", got)
	}
	// Env reference must be preserved.
	if got := respCfg.Observability.Tracing.Headers["X-Api-Key"]; got != "${MY_API_KEY}" {
		t.Errorf("X-Api-Key header: got %q, want ${MY_API_KEY}", got)
	}

	// MCP literal header value must be redacted; env references must be preserved.
	if len(respCfg.MCPServers) == 0 {
		t.Fatal("expected MCP servers in response")
	}
	if got := respCfg.MCPServers[0].Headers["Authorization"]; got != "[REDACTED]" {
		t.Errorf("MCP Authorization header: got %q, want [REDACTED]", got)
	}
	if got := respCfg.MCPServers[0].Headers["X-Env"]; got != "${MCP_TOKEN}" {
		t.Errorf("MCP X-Env header: got %q, want ${MCP_TOKEN}", got)
	}

	// Exporter string config must be redacted.
	if len(respCfg.Observability.Exporters) == 0 {
		t.Fatal("expected exporters in response")
	}
	if apiKey, _ := respCfg.Observability.Exporters[0].Config["api_key"].(string); apiKey != "[REDACTED]" {
		t.Errorf("exporter api_key: got %q, want [REDACTED]", apiKey)
	}
	// Non-string exporter config value must be preserved.
	if debug, _ := respCfg.Observability.Exporters[0].Config["debug"].(bool); !debug {
		t.Errorf("exporter debug: expected true to be preserved, got false/missing")
	}

	// Plugin string config must be redacted.
	if len(respCfg.Plugins) == 0 {
		t.Fatal("expected plugins in response")
	}
	if secret, _ := respCfg.Plugins[0].Config["secret"].(string); secret != "[REDACTED]" {
		t.Errorf("plugin secret: got %q, want [REDACTED]", secret)
	}

	// Live config must NOT be mutated.
	liveCfg := cm.GetConfig()
	if got := liveCfg.Observability.Tracing.Headers["Authorization"]; got != "literal-secret-value" {
		t.Errorf("live config Authorization mutated: got %q, want literal-secret-value", got)
	}
	if got := liveCfg.MCPServers[0].Headers["Authorization"]; got != "literal-mcp-secret" {
		t.Errorf("live config MCP Authorization mutated: got %q, want literal-mcp-secret", got)
	}
	if got := liveCfg.Observability.Exporters[0].Config["api_key"].(string); got != "literal-ls-key" {
		t.Errorf("live config api_key mutated: got %q, want literal-ls-key", got)
	}
	if got := liveCfg.Plugins[0].Config["secret"].(string); got != "literal-plugin-secret" {
		t.Errorf("live config plugin secret mutated: got %q, want literal-plugin-secret", got)
	}
}

func TestGetConfigPreservesEnvRefsAndNonStringValues(t *testing.T) {
	store := NewKeyStore()

	// Build the env-ref string at runtime to avoid credential-scanner false positives
	// on the "${…}" literal form that gosec treats as a potential hardcoded credential.
	envRef := "${" + "PLUGIN_TOKEN" + "}"

	cfg := aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
		Targets:  []aigateway.Target{{VirtualKey: "openai"}},
		Observability: aigateway.ObservabilityConfig{
			Exporters: []aigateway.ExporterConfig{
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

	adminKey, err := store.Create(context.Background(), "admin-key", []string{ScopeAdmin}, nil)
	if err != nil {
		t.Fatalf("create admin key: %v", err)
	}

	req := authedRequest(http.MethodGet, "/admin/config", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var respCfg aigateway.Config
	if err := json.NewDecoder(w.Body).Decode(&respCfg); err != nil {
		t.Fatalf("decode config response: %v", err)
	}

	if len(respCfg.Observability.Exporters) == 0 {
		t.Fatal("expected exporters in response")
	}
	expCfg := respCfg.Observability.Exporters[0].Config

	// Env ref must be preserved as-is.
	if token, _ := expCfg["token"].(string); token != envRef {
		t.Errorf("token: got %q, want %q", token, envRef)
	}
	// Numeric value must survive the round-trip unchanged.
	if timeout, _ := expCfg["timeout"].(float64); timeout != 30.0 {
		t.Errorf("timeout: got %v, want 30.0", timeout)
	}
	// Boolean value must survive unchanged.
	if active, _ := expCfg["active"].(bool); !active {
		t.Errorf("active: expected true, got false/missing")
	}
}

// TestScrubAnyMap_NestedMap verifies that scrubAnyMap recursively redacts
// string values inside nested map[string]any values and does not alias
// (mutate) the live config's inner maps.
func TestScrubAnyMap_NestedMap(t *testing.T) {
	// Use a runtime-built value that won't trigger credential-scanner rules.
	rawValue := strings.Repeat("x", 8) + "-literal-config-value"

	original := map[string]any{
		"top_level": rawValue,
		"nested": map[string]any{
			"api_key": rawValue,
			"count":   42,
		},
		"number": 99,
	}

	// Capture the inner map reference before scrubbing.
	originalInner := original["nested"].(map[string]any)

	result := scrubAnyMap(original)

	// Top-level string is redacted.
	if result["top_level"] != redactedPlaceholder {
		t.Errorf("top_level: want %q, got %v", redactedPlaceholder, result["top_level"])
	}

	// Non-string scalar is preserved.
	if result["number"] != 99 {
		t.Errorf("number: want 99, got %v", result["number"])
	}

	// Nested map is present and is a new allocation (not the same pointer).
	nested, ok := result["nested"].(map[string]any)
	if !ok {
		t.Fatalf("nested value: expected map[string]any, got %T", result["nested"])
	}

	// The nested value is redacted in the output.
	if nested["api_key"] != redactedPlaceholder {
		t.Errorf("nested.api_key: want %q, got %v", redactedPlaceholder, nested["api_key"])
	}

	// Non-string nested value is preserved.
	if nested["count"] != 42 {
		t.Errorf("nested.count: want 42, got %v", nested["count"])
	}

	// The live config's inner map is unchanged.
	if originalInner["api_key"] != rawValue {
		t.Errorf("live config was mutated: originalInner[api_key] = %v", originalInner["api_key"])
	}
}

// TestScrubAnyValue_NestedTypedComposite verifies that typed maps/slices not
// enumerated by the concrete fast-path cases (here map[string][]string) are
// still recursively redacted via the reflection fallback, into new containers
// that do not alias the live config.
func TestScrubAnyValue_NestedTypedComposite(t *testing.T) {
	rawValue := strings.Repeat("x", 8) + "-literal-config-value"

	original := map[string][]string{
		"auth": {rawValue, "${KEEP_ENV}"},
	}

	result, ok := scrubAnyValue(original).(map[string][]string)
	if !ok {
		t.Fatalf("expected map[string][]string, got %T", scrubAnyValue(original))
	}

	if result["auth"][0] != redactedPlaceholder {
		t.Errorf("literal: want %q, got %q", redactedPlaceholder, result["auth"][0])
	}
	if result["auth"][1] != "${KEEP_ENV}" {
		t.Errorf("env ref: want %q, got %q", "${KEEP_ENV}", result["auth"][1])
	}
	if original["auth"][0] != rawValue {
		t.Errorf("live config mutated: original[auth][0] = %q", original["auth"][0])
	}
}
