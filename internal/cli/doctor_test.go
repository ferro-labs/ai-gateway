package cli

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunDoctor(t *testing.T) {
	// clearProviderKeys blanks every provider env var doctor probes so host
	// environment leakage does not skew the "N found" count.
	clearProviderKeys := func(t *testing.T) {
		for _, k := range []string{
			"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GEMINI_API_KEY",
			"GROQ_API_KEY", "MISTRAL_API_KEY",
		} {
			t.Setenv(k, "")
		}
	}

	t.Run("reports keys, config, auth and healthy connectivity", func(t *testing.T) {
		srv := stubGateway(t, map[string]http.HandlerFunc{
			"/health": jsonHandler(http.StatusOK, `{"status":"ok"}`),
		})
		cmd, out := newHandlerCmd(t, srv.URL, "table")
		clearProviderKeys(t)
		t.Setenv("OPENAI_API_KEY", "sk-test")
		t.Setenv("GATEWAY_CONFIG", "")
		t.Setenv("MASTER_KEY", "master-test")

		if err := runDoctor(cmd, nil); err != nil {
			t.Fatalf("runDoctor: %v", err)
		}

		got := out.String()
		for _, want := range []string{
			"Provider API Keys", "openai", "1 found",
			"MASTER_KEY is set", "healthy",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("output missing %q:\n%s", want, got)
			}
		}
	})

	t.Run("flags no keys and an invalid config file", func(t *testing.T) {
		cfgPath := filepath.Join(t.TempDir(), "bad.yaml")
		// Valid YAML, invalid config: an unknown strategy mode fails validation.
		if err := os.WriteFile(cfgPath, []byte("strategy:\n  mode: bogus\ntargets:\n  - virtual_key: openai\n"), 0600); err != nil {
			t.Fatalf("write config: %v", err)
		}

		cmd, out := newHandlerCmd(t, "http://127.0.0.1:1", "table")
		clearProviderKeys(t)
		t.Setenv("GATEWAY_CONFIG", cfgPath)
		t.Setenv("MASTER_KEY", "")

		if err := runDoctor(cmd, nil); err != nil {
			t.Fatalf("runDoctor: %v", err)
		}

		got := out.String()
		for _, want := range []string{
			"no provider API keys detected",
			cfgPath,
			"MASTER_KEY not set",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("output missing %q:\n%s", want, got)
			}
		}
	})
}
