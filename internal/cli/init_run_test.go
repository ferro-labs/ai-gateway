package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// newInitCmd builds a command carrying init's own flags, with stderr captured.
func newInitCmd(t *testing.T) (*cobra.Command, *bytes.Buffer) {
	t.Helper()
	t.Setenv("NO_COLOR", "1")
	cmd := &cobra.Command{Use: "init"}
	cmd.Flags().String("config-format", "yaml", "")
	cmd.Flags().StringP("output", "o", "", "")
	cmd.Flags().Bool("non-interactive", false, "")
	buf := &bytes.Buffer{}
	cmd.SetErr(buf)
	return cmd, buf
}

func TestRunInit(t *testing.T) {
	t.Run("creates the config and prints a master key", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.yaml")
		cmd, errOut := newInitCmd(t)
		if err := cmd.Flags().Set("output", path); err != nil {
			t.Fatalf("set output: %v", err)
		}
		if err := cmd.Flags().Set("non-interactive", "true"); err != nil {
			t.Fatalf("set non-interactive: %v", err)
		}

		if err := runInit(cmd, nil); err != nil {
			t.Fatalf("runInit: %v", err)
		}

		if _, err := os.Stat(path); err != nil {
			t.Fatalf("config file not written: %v", err)
		}
		got := errOut.String()
		for _, want := range []string{"Created", "Master key:", "fgw_"} {
			if !strings.Contains(got, want) {
				t.Errorf("stderr missing %q:\n%s", want, got)
			}
		}
	})

	t.Run("does not overwrite an existing config", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte("existing: true\n"), 0600); err != nil {
			t.Fatalf("seed config: %v", err)
		}
		cmd, errOut := newInitCmd(t)
		if err := cmd.Flags().Set("output", path); err != nil {
			t.Fatalf("set output: %v", err)
		}
		if err := cmd.Flags().Set("non-interactive", "true"); err != nil {
			t.Fatalf("set non-interactive: %v", err)
		}

		if err := runInit(cmd, nil); err != nil {
			t.Fatalf("runInit: %v", err)
		}

		if !strings.Contains(errOut.String(), "skipped") {
			t.Errorf("want skip notice, got:\n%s", errOut.String())
		}
		// The pre-existing content must survive.
		data, err := os.ReadFile(path) //nolint:gosec // G304: path is a test-local temp file
		if err != nil {
			t.Fatalf("read config: %v", err)
		}
		if !strings.Contains(string(data), "existing: true") {
			t.Errorf("existing config was overwritten: %s", data)
		}
	})
}
