package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return p
}

func TestRunValidate(t *testing.T) {
	const validYAML = "strategy:\n  mode: fallback\ntargets:\n  - virtual_key: openai\n"

	t.Run("valid config renders a summary", func(t *testing.T) {
		path := writeConfig(t, "config.yaml", validYAML)
		cmd, out := newHandlerCmd(t, "", "table")

		if err := runValidate(cmd, []string{path}); err != nil {
			t.Fatalf("runValidate: %v", err)
		}
		got := out.String()
		for _, want := range []string{"Config is valid", "fallback", "openai"} {
			if !strings.Contains(got, want) {
				t.Errorf("output missing %q:\n%s", want, got)
			}
		}
	})

	t.Run("json format prints the parsed config", func(t *testing.T) {
		path := writeConfig(t, "config.yaml", validYAML)
		cmd, out := newHandlerCmd(t, "", "json")

		if err := runValidate(cmd, []string{path}); err != nil {
			t.Fatalf("runValidate: %v", err)
		}
		if !strings.Contains(out.String(), "fallback") {
			t.Errorf("want JSON config, got:\n%s", out.String())
		}
	})

	t.Run("invalid config returns a validation error", func(t *testing.T) {
		path := writeConfig(t, "config.yaml", "strategy:\n  mode: bogus\ntargets:\n  - virtual_key: openai\n")
		cmd, _ := newHandlerCmd(t, "", "table")

		err := runValidate(cmd, []string{path})
		if err == nil || !strings.Contains(err.Error(), "validation failed") {
			t.Fatalf("want validation error, got %v", err)
		}
	})

	t.Run("missing file returns a load error", func(t *testing.T) {
		cmd, _ := newHandlerCmd(t, "", "table")

		err := runValidate(cmd, []string{filepath.Join(t.TempDir(), "nope.yaml")})
		if err == nil || !strings.Contains(err.Error(), "load config") {
			t.Fatalf("want load error, got %v", err)
		}
	})
}
