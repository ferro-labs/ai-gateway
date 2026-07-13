package aigateway

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExpandEnvRefs_LeavesLiteralDollarsIntact is the regression guard for the
// os.Expand corruption. os.Expand treated $1/$$/$w0rd as shell variables and ate
// them, so a word-filter blocked word "$100" became "00" (silently weakening a
// guardrail) and a generated password "pa$$w0rd" became "paw0rd".
func TestExpandEnvRefs_LeavesLiteralDollarsIntact(t *testing.T) {
	t.Setenv("SET_VAR", "value")

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"blocked word with leading dollar", "$100", "$100"},
		{"password with doubled dollars", "pa$$w0rd", "pa$$w0rd"},
		{"price in a sentence", "costs $5 per 1M", "costs $5 per 1M"},
		{"trailing dollar", "cash$", "cash$"},
		{"complex password", "P@ss$w0rd!", "P@ss$w0rd!"},
		{"brace form is substituted", "${SET_VAR}", "value"},
		{"brace form inside text", "Bearer ${SET_VAR}", "Bearer value"},
		{"bare dollar-name is NOT a reference", "$SET_VAR", "$SET_VAR"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := expandEnvRefs(tt.in)
			if err != nil {
				t.Fatalf("expandEnvRefs(%q): %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("expandEnvRefs(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestExpandEnvRefs_UnsetVariableIsAnError proves an unset variable fails loudly at
// load instead of silently blanking a secret or a guardrail rule.
func TestExpandEnvRefs_UnsetVariableIsAnError(t *testing.T) {
	if err := os.Unsetenv("DEFINITELY_NOT_SET_XYZ"); err != nil {
		t.Fatalf("unsetenv: %v", err)
	}
	_, err := expandEnvRefs("Bearer ${DEFINITELY_NOT_SET_XYZ}")
	if err == nil {
		t.Fatal("an unset ${VAR} must be an error, not a silent empty string")
	}
	if !strings.Contains(err.Error(), "DEFINITELY_NOT_SET_XYZ") {
		t.Errorf("the error must name the offending variable, got: %v", err)
	}
}

// TestLoadConfig_GuardrailWordsSurviveDollarSigns drives the real loader end to end:
// a word-filter blocked-word list containing literal dollars must reach the plugin
// byte-for-byte, or the guardrail is silently weakened.
func TestLoadConfig_GuardrailWordsSurviveDollarSigns(t *testing.T) {
	t.Setenv("MY_TOKEN", "s3cret")

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
strategy:
  mode: single
targets:
  - virtual_key: openai
plugins:
  - name: word-filter
    type: guardrail
    stage: before_request
    enabled: true
    config:
      blocked_words: ["$100", "pa$$word", "cash$"]
      api_token: "${MY_TOKEN}"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	words, ok := cfg.Plugins[0].Config["blocked_words"].([]any)
	if !ok {
		t.Fatalf("blocked_words has unexpected type %T", cfg.Plugins[0].Config["blocked_words"])
	}
	want := []string{"$100", "pa$$word", "cash$"}
	for i, w := range want {
		if got := words[i].(string); got != w {
			t.Errorf("blocked_words[%d] = %q, want %q — the guardrail was silently corrupted", i, got, w)
		}
	}
	if got := cfg.Plugins[0].Config["api_token"]; got != "s3cret" {
		t.Errorf("api_token = %v, want the substituted secret", got)
	}
}
