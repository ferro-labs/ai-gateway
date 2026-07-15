package cli

import (
	"strings"
	"testing"
)

func TestRunVersion(t *testing.T) {
	t.Run("table format lists build fields", func(t *testing.T) {
		cmd, out := newHandlerCmd(t, "", "table")

		if err := runVersion(cmd, nil); err != nil {
			t.Fatalf("runVersion: %v", err)
		}
		got := out.String()
		for _, want := range []string{"Version", "Commit", "Go"} {
			if !strings.Contains(got, want) {
				t.Errorf("output missing %q:\n%s", want, got)
			}
		}
	})

	t.Run("json format emits a machine-readable object", func(t *testing.T) {
		cmd, out := newHandlerCmd(t, "", "json")

		if err := runVersion(cmd, nil); err != nil {
			t.Fatalf("runVersion: %v", err)
		}
		if !strings.Contains(out.String(), `"version"`) {
			t.Errorf("want json version key, got:\n%s", out.String())
		}
	})
}
