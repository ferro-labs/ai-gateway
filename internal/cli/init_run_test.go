package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// newInitCmd builds a command carrying init's own flags, mounted under a root
// carrying the persistent flags cmd/ferrogw/main.go registers, with stderr
// captured. init writes its report to stderr, so that is the buffer to read.
func newInitCmd(t *testing.T) (*cobra.Command, *bytes.Buffer) {
	t.Helper()
	t.Setenv("NO_COLOR", "1")
	root := &cobra.Command{Use: "ferrogw"}
	root.PersistentFlags().String(FlagFormat, FormatTable, "")
	cmd := &cobra.Command{Use: "init"}
	cmd.Flags().String("config-format", "yaml", "")
	cmd.Flags().StringP("output", "o", "", "")
	cmd.Flags().Bool("non-interactive", false, "")
	root.AddCommand(cmd)
	buf := &bytes.Buffer{}
	cmd.SetErr(buf)
	cmd.SetOut(buf)
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

	// The gateway loads a config file only when GATEWAY_CONFIG points at one, so
	// the next steps have to name the file that was actually written.
	t.Run("next steps export GATEWAY_CONFIG for the path it wrote", func(t *testing.T) {
		dir := t.TempDir()
		tests := []struct {
			name   string
			format string
			output string
			want   string
		}{
			{name: "default yaml path", format: "yaml", output: "", want: "config.yaml"},
			{name: "default json path", format: "json", output: "", want: "config.json"},
			{name: "custom output path", format: "yaml", output: filepath.Join(dir, "custom.yaml"), want: filepath.Join(dir, "custom.yaml")},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if tt.output == "" {
					t.Chdir(t.TempDir())
				}
				cmd, errOut := newInitCmd(t)
				if err := cmd.Flags().Set("config-format", tt.format); err != nil {
					t.Fatalf("set config-format: %v", err)
				}
				if err := cmd.Flags().Set("output", tt.output); err != nil {
					t.Fatalf("set output: %v", err)
				}
				if err := cmd.Flags().Set("non-interactive", "true"); err != nil {
					t.Fatalf("set non-interactive: %v", err)
				}

				if err := runInit(cmd, nil); err != nil {
					t.Fatalf("runInit: %v", err)
				}

				got := errOut.String()
				if !strings.Contains(got, "Created "+tt.want) {
					t.Errorf("stderr does not name the file written (%s):\n%s", tt.want, got)
				}
				if !strings.Contains(got, "export GATEWAY_CONFIG="+tt.want) {
					t.Errorf("stderr missing export GATEWAY_CONFIG=%s:\n%s", tt.want, got)
				}
			})
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
		// OPS-011(d): the key is persisted nowhere, so one printed by a run
		// that wrote no file is a credential that reads as live and
		// authenticates against nothing.
		for _, unwanted := range []string{"fgw_", "Master key", "MASTER_KEY="} {
			if strings.Contains(errOut.String(), unwanted) {
				t.Errorf("a skipped init must not emit a master key (found %q):\n%s", unwanted, errOut.String())
			}
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

	// init has its own --config-format; the inherited --format selects the
	// encoding of a *result*, which init does not produce. Silently ignoring it
	// meant `ferrogw init --format json` looked like it had chosen JSON.
	t.Run("a non-table --format is refused and names --config-format", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.yaml")
		cmd, _ := newInitCmd(t)
		if err := cmd.Root().PersistentFlags().Set(FlagFormat, FormatJSON); err != nil {
			t.Fatalf("set format: %v", err)
		}
		if err := cmd.Flags().Set("output", path); err != nil {
			t.Fatalf("set output: %v", err)
		}

		err := runInit(cmd, nil)
		if err == nil {
			t.Fatal("runInit must refuse --format json rather than ignore it")
		}
		if !strings.Contains(err.Error(), "--config-format") {
			t.Errorf("error = %v, want it to point at --config-format", err)
		}
		if _, statErr := os.Stat(path); statErr == nil {
			t.Error("a refused invocation must not write a config file")
		}
	})
}
