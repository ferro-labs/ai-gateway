package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ferro-labs/ai-gateway/config"
)

// TestLoadConfig_KeepsEnvReferencesUnresolved is the guard for the secret-at-rest
// leak. The Config travels a long way: it is persisted to the config-history store,
// returned by GET /admin/config, and re-saved on rollback. If LoadConfig materialised
// secrets into it, a failed admin config apply would write real API keys into the
// database in plaintext. References must survive in the Config; only the constructed
// component ever sees the value.
func TestLoadConfig_KeepsEnvReferencesUnresolved(t *testing.T) {
	t.Setenv("FERRO_TEST_SECRET", "super-secret-key")

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
strategy:
  mode: single
targets:
  - virtual_key: openai
mcp_servers:
  - name: database
    url: https://mcp-db.internal/mcp
    headers:
      Authorization: "Bearer ${FERRO_TEST_SECRET}"
plugins:
  - name: request-logger
    type: logging
    stage: before_request
    enabled: true
    config:
      api_key: "${FERRO_TEST_SECRET}"
      blocked_words: ["$100", "pa$$word"]
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	// The secret must NOT appear anywhere in the loaded Config.
	if got := cfg.Plugins[0].Config["api_key"]; got != "${FERRO_TEST_SECRET}" {
		t.Errorf("plugin api_key = %v; the Config must keep the ${VAR} reference, never the secret", got)
	}
	if got := cfg.MCPServers[0].Headers["Authorization"]; got != "Bearer ${FERRO_TEST_SECRET}" {
		t.Errorf("MCP header = %q; the Config must keep the ${VAR} reference, never the secret", got)
	}

	// And a literal '$' is data, preserved byte-for-byte.
	words, ok := cfg.Plugins[0].Config["blocked_words"].([]any)
	if !ok {
		t.Fatalf("blocked_words type %T", cfg.Plugins[0].Config["blocked_words"])
	}
	for i, want := range []string{"$100", "pa$$word"} {
		if got := words[i].(string); got != want {
			t.Errorf("blocked_words[%d] = %q, want %q", i, got, want)
		}
	}
}
