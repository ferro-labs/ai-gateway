package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/internal/admin/repository"
	"github.com/ferro-labs/ai-gateway/mcp"
)

// redactedPlaceholder mirrors the unexported repository constant of the same
// name (repository/scrub.go): the marker ScrubConfigSecrets substitutes for a
// redacted value. Kept in sync here so these cross-package handler tests can
// assert against the exact scrub output.
const redactedPlaceholder = "[REDACTED]"

// TestRedactedSecretField covers every field scrubConfigSecrets redacts, so the
// two lists cannot drift apart silently: anything scrubbed on the way out must
// be refused on the way back in.
func TestRedactedSecretField(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.Config
		want string
	}{
		{
			name: "clean config",
			cfg:  config.Config{},
			want: "",
		},
		{
			name: "tracing headers",
			cfg: config.Config{Observability: config.ObservabilityConfig{
				Tracing: config.TracingConfig{Headers: map[string]string{"authorization": redactedPlaceholder}},
			}},
			want: "observability.tracing.headers",
		},
		{
			name: "mcp server headers",
			cfg: config.Config{MCPServers: []mcp.ServerConfig{
				{Name: "a"},
				{Name: "b", Headers: map[string]string{"Authorization": redactedPlaceholder}},
			}},
			want: "mcp_servers[1].headers",
		},
		{
			name: "mcp server env",
			cfg: config.Config{MCPServers: []mcp.ServerConfig{
				{Name: "a", Env: map[string]string{"TOKEN": redactedPlaceholder}},
			}},
			want: "mcp_servers[0].env",
		},
		{
			name: "exporter config",
			cfg: config.Config{Observability: config.ObservabilityConfig{
				Exporters: []config.ExporterConfig{
					{Name: "langsmith", Config: map[string]any{"api_key": redactedPlaceholder}},
				},
			}},
			want: "observability.exporters[0].config",
		},
		{
			name: "plugin config",
			cfg: config.Config{Plugins: []config.PluginConfig{
				{Name: "budget", Config: map[string]any{"token": redactedPlaceholder}},
			}},
			want: "plugins[0].config",
		},
		{
			name: "nested inside a plugin config map",
			cfg: config.Config{Plugins: []config.PluginConfig{
				{Name: "p", Config: map[string]any{
					"upstream": map[string]any{"auth": map[string]any{"key": redactedPlaceholder}},
				}},
			}},
			want: "plugins[0].config",
		},
		{
			name: "nested inside a slice in a plugin config",
			cfg: config.Config{Plugins: []config.PluginConfig{
				{Name: "p", Config: map[string]any{"keys": []any{"real", redactedPlaceholder}}},
			}},
			want: "plugins[0].config",
		},
		{
			name: "env references are not placeholders",
			cfg: config.Config{MCPServers: []mcp.ServerConfig{
				//nolint:gosec // G101: an unresolved ${VAR} reference in a fixture, not a credential
				{Name: "a", Env: map[string]string{"TOKEN": "${SOME_TOKEN}"}},
			}},
			want: "",
		},
		{
			// A map value with an innocuous key keeps its shape and loses only
			// its secret parts, so a redaction token arrives back surrounded by
			// the text it was cut from. The guard therefore matches the token
			// wherever it sits, exactly as it always has for scalar fields —
			// which also means a value that merely quotes the token is refused.
			// That costs the operator one edit; missing it would overwrite a
			// live credential with the placeholder.
			name: "a token anywhere in a map value is refused",
			cfg: config.Config{MCPServers: []mcp.ServerConfig{
				{Name: "a", Env: map[string]string{"NOTE": "see [REDACTED] in the docs"}},
			}},
			want: "mcp_servers[0].env",
		},
		{
			name: "settings served readable are not refused",
			cfg: config.Config{
				Aliases: map[string]string{"fast": "gpt-4o-mini"},
				Plugins: []config.PluginConfig{
					{Name: "word-filter", Config: map[string]any{"blocked_words": []any{"password"}}},
					{Name: "request-logger", Config: map[string]any{"level": "info", "persist": true}},
				},
			},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := repository.RedactedSecretField(tt.cfg); got != tt.want {
				t.Fatalf("redactedSecretField() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestUpdateConfigRejectsRedactedRoundTrip is the bug this guards against, end
// to end: read the config, send it back unmodified, and the literal secret that
// the read replaced with "[REDACTED]" must not overwrite the live value.
func TestUpdateConfigRejectsRedactedRoundTrip(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)

	// Seed a config carrying a literal secret.
	seeded := config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "openai"}},
		MCPServers: []mcp.ServerConfig{
			{Name: "search", URL: "https://mcp.example.com/mcp", Headers: map[string]string{"Authorization": "Bearer real-secret"}},
		},
	}
	seededBody, err := json.Marshal(seeded)
	if err != nil {
		t.Fatalf("marshal seeded config: %v", err)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, authedRequest(http.MethodPut, "/admin/config", string(seededBody), adminKey))
	if w.Code != http.StatusOK {
		t.Fatalf("seeding config: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Read it back — the secret is now the placeholder.
	getW := httptest.NewRecorder()
	r.ServeHTTP(getW, authedRequest(http.MethodGet, "/admin/config", "", adminKey))
	readBack := getW.Body.String()
	if !strings.Contains(readBack, redactedPlaceholder) {
		t.Fatalf("expected read to redact the secret, got: %s", readBack)
	}

	// Send exactly that back, as the dashboard config editor would.
	putW := httptest.NewRecorder()
	r.ServeHTTP(putW, authedRequest(http.MethodPut, "/admin/config", readBack, adminKey))

	if putW.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a redacted round-trip, got %d: %s", putW.Code, putW.Body.String())
	}
	body := putW.Body.String()
	if !strings.Contains(body, "redacted_secret") {
		t.Fatalf("expected redacted_secret code, got: %s", body)
	}
	if !strings.Contains(body, "mcp_servers[0].headers") {
		t.Fatalf("expected the offending field named, got: %s", body)
	}

	// The live secret survived: reading again still shows a redacted value
	// rather than the placeholder having become the stored value.
	finalW := httptest.NewRecorder()
	r.ServeHTTP(finalW, authedRequest(http.MethodGet, "/admin/config", "", adminKey))
	var final config.Config
	decodeJSON(t, finalW.Body, &final)
	if len(final.MCPServers) != 1 {
		t.Fatalf("expected the seeded MCP server to survive, got %d", len(final.MCPServers))
	}
}
