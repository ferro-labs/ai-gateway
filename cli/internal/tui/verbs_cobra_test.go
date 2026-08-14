// This file is deliberately in the external test package: internal/command is
// the package that hands bare `ferro` off to internal/tui, so internal/tui must
// never import it back. An external test may, which is how the TUI's vocabulary
// gets asserted against the real scriptable tree with no import cycle.
package tui_test

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/cli/internal/command"
	"github.com/ferro-labs/ai-gateway/cli/internal/tui"
)

// The composer completes what the CLI can actually run. If a verb is added,
// renamed or removed in internal/command, this is where the TUI finds out.
func TestVerbsMatchTheScriptableTree(t *testing.T) {
	v := tui.Verbs(command.NewRoot())
	for _, want := range []string{
		"status", "keys", "keys create", "keys rotate", "keys revoke",
		"logs", "logs tail", "logs stats", "models", "providers",
		"mcp", "plugins", "sessions", "audit",
		"services", "chat", "version",
	} {
		if !slices.Contains(v, want) {
			t.Fatalf("the TUI vocabulary has drifted from the cobra tree: missing %q\ngot %v", want, v)
		}
	}
	if slices.Contains(v, "completion") || slices.Contains(v, "help completion") {
		t.Fatalf("shell-plumbing commands are not TUI verbs, got %v", v)
	}
}

// tui.TableRows and command.Printer.Table both call internal/table's shared
// layout now (table.Rows and table.Write, the latter also underneath the
// former), so the two sides of this comparison can no longer drift on their
// own — the whole point of moving the layout into that leaf package. This
// test earns its keep anyway, cheaply, as the trip-wire against the failure
// mode that motivated the move: it fails the moment either call site stops
// routing through internal/table and grows a layout — or a special case — of
// its own again, which is exactly how the two copies drifted before. The
// Dim-row assertions below are independent of the parity question and still
// exercise the console's own furniture-vs-output contract.
func TestConsoleTableMatchesThePipedTable(t *testing.T) {
	headers := []string{"NAME", "READY", "REQUIRED", "LAST ERROR"}
	cells := [][]string{
		{"filesystem", "yes", "no", "-"},
		{"search", "no", "yes", "dial tcp: connection refused"},
		{"a-considerably-longer-server-name", "yes", "no", "-"},
	}

	var buf bytes.Buffer
	(&command.Printer{Out: &buf}).Table(headers, cells)
	piped := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	for i, line := range piped {
		// The console trims the trailing padding the last column carries;
		// nothing else about the two layouts may differ.
		piped[i] = strings.TrimRight(line, " ")
	}

	rows := tui.TableRows(headers, cells)
	console := make([]string, 0, len(rows))
	for _, r := range rows {
		console = append(console, r.Text)
	}

	if !slices.Equal(console, piped) {
		t.Fatalf("the console table has drifted from the piped one:\nconsole: %q\npiped:   %q", console, piped)
	}
	if len(rows) < 2 || !rows[0].Dim || !rows[1].Dim {
		t.Fatalf("the heading and its rule are furniture and must be dimmed, got %+v", rows[:min(2, len(rows))])
	}
	for _, r := range rows[2:] {
		if r.Dim {
			t.Fatalf("a data row is the output, not furniture: %+v", r)
		}
	}
}
