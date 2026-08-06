package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/providers"
)

// clearProviderEnv blanks every environment variable any built-in provider
// reads, so a scaffolding test sees only the credentials it sets itself and not
// whatever happens to be exported in the developer's shell.
func clearProviderEnv(t *testing.T) {
	t.Helper()
	for _, entry := range providers.AllProviders() {
		for _, mapping := range entry.EnvMappings {
			t.Setenv(mapping.EnvVar, "")
		}
	}
}

// writeAndLoad scaffolds a config in the given format and returns it parsed
// back through the same LoadConfig/ValidateConfig path `ferrogw validate` uses.
func writeAndLoad(t *testing.T, format string) (*config.Config, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config."+format)
	if err := WriteDefaultConfig(path, format); err != nil {
		t.Fatalf("WriteDefaultConfig(%s): %v", format, err)
	}
	raw, err := os.ReadFile(path) //nolint:gosec // G304: path is from t.TempDir()
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("generated %s config does not parse: %v\n%s", format, err, raw)
	}
	if err := config.ValidateConfig(*cfg); err != nil {
		t.Fatalf("generated %s config does not validate: %v\n%s", format, err, raw)
	}
	return cfg, string(raw)
}

func targetKeys(cfg *config.Config) []string {
	keys := make([]string, 0, len(cfg.Targets))
	for _, target := range cfg.Targets {
		keys = append(keys, target.VirtualKey)
	}
	return keys
}

// TestWriteDefaultConfig_ScaffoldsOnlyCredentialedTargets is the core of the
// fix: a scaffolded target must be one this environment can actually serve.
func TestWriteDefaultConfig_ScaffoldsOnlyCredentialedTargets(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want []string
	}{
		{
			name: "no credentials falls back to a labelled placeholder",
			env:  nil,
			want: []string{placeholderProvider},
		},
		{
			name: "one credential yields exactly that target",
			env:  map[string]string{"GROQ_API_KEY": "gsk-test"},
			want: []string{"groq"},
		},
		{
			// Registry order, not the order the keys were exported: the file is
			// a fallback chain and the gateway's own provider order is the only
			// non-arbitrary one available.
			name: "several credentials yield all of them in registry order",
			env: map[string]string{
				"OPENAI_API_KEY":  "sk-test",
				"GROQ_API_KEY":    "gsk-test",
				"MISTRAL_API_KEY": "mk-test",
			},
			want: []string{"groq", "mistral", "openai"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, format := range []string{FormatYAML, FormatJSON} {
				t.Run(format, func(t *testing.T) {
					clearProviderEnv(t)
					for key, value := range tt.env {
						t.Setenv(key, value)
					}

					cfg, _ := writeAndLoad(t, format)
					got := targetKeys(cfg)
					if strings.Join(got, ",") != strings.Join(tt.want, ",") {
						t.Errorf("targets = %v, want %v", got, tt.want)
					}
				})
			}
		})
	}
}

// TestWriteDefaultConfig_NoUncredentialedTarget guards the reported symptom
// directly: anthropic must not appear as a live target when its key is absent.
func TestWriteDefaultConfig_NoUncredentialedTarget(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("OPENAI_API_KEY", "sk-test")

	for _, format := range []string{FormatYAML, FormatJSON} {
		t.Run(format, func(t *testing.T) {
			cfg, _ := writeAndLoad(t, format)
			for _, key := range targetKeys(cfg) {
				if key == "anthropic" {
					t.Fatalf("anthropic scaffolded as a target without ANTHROPIC_API_KEY: %v", targetKeys(cfg))
				}
			}
		})
	}
}

// TestWriteDefaultConfig_PlaceholderIsLabelled keeps the zero-credential file
// honest: it still validates, but it must say the target is a placeholder
// rather than present it as something the operator can use.
func TestWriteDefaultConfig_PlaceholderIsLabelled(t *testing.T) {
	clearProviderEnv(t)

	_, raw := writeAndLoad(t, FormatYAML)
	if !strings.Contains(raw, "placeholder") {
		t.Errorf("zero-credential config does not label its placeholder target:\n%s", raw)
	}
}

// TestWriteDefaultConfig_KeepsTeachingTheFormat guards against the generated
// file becoming so dynamic it stops documenting the schema for a first reader.
func TestWriteDefaultConfig_KeepsTeachingTheFormat(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("OPENAI_API_KEY", "sk-test")

	_, raw := writeAndLoad(t, FormatYAML)
	for _, want := range []string{"mode: fallback", "weight", "retry", "plugins"} {
		if !strings.Contains(raw, want) {
			t.Errorf("generated config no longer documents %q:\n%s", want, raw)
		}
	}
}

// TestRunInit_ReportsScaffoldedTargets covers the other half of the finding:
// the operator is told which targets were written and why.
func TestRunInit_ReportsScaffoldedTargets(t *testing.T) {
	t.Run("names the credentials it found", func(t *testing.T) {
		clearProviderEnv(t)
		t.Setenv("GROQ_API_KEY", "gsk-test")

		cmd, errOut := newInitCmd(t)
		if err := cmd.Flags().Set("output", filepath.Join(t.TempDir(), "config.yaml")); err != nil {
			t.Fatalf("set output: %v", err)
		}
		if err := cmd.Flags().Set("non-interactive", "true"); err != nil {
			t.Fatalf("set non-interactive: %v", err)
		}
		if err := runInit(cmd, nil); err != nil {
			t.Fatalf("runInit: %v", err)
		}
		if !strings.Contains(errOut.String(), "groq") {
			t.Errorf("stderr does not name the scaffolded target:\n%s", errOut.String())
		}
	})

	t.Run("says so when no credentials are present", func(t *testing.T) {
		clearProviderEnv(t)

		cmd, errOut := newInitCmd(t)
		if err := cmd.Flags().Set("output", filepath.Join(t.TempDir(), "config.yaml")); err != nil {
			t.Fatalf("set output: %v", err)
		}
		if err := cmd.Flags().Set("non-interactive", "true"); err != nil {
			t.Fatalf("set non-interactive: %v", err)
		}
		if err := runInit(cmd, nil); err != nil {
			t.Fatalf("runInit: %v", err)
		}
		got := errOut.String()
		if !strings.Contains(got, "No provider credentials") {
			t.Errorf("stderr does not report the missing credentials:\n%s", got)
		}
		// The master key must still be generated regardless.
		if !strings.Contains(got, "fgw_") {
			t.Errorf("stderr missing master key:\n%s", got)
		}
	})
}
