package cli

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

// forceTerminal pins the TTY answer for one test. A test binary's stdout is a
// pipe, so without this every colour assertion would be testing the non-TTY
// path twice.
func forceTerminal(t *testing.T, isTTY bool) {
	t.Helper()
	orig := stdoutIsTerminal
	stdoutIsTerminal = func() bool { return isTTY }
	t.Cleanup(func() { stdoutIsTerminal = orig })
}

func TestClr(t *testing.T) {
	t.Run("passes text through when NO_COLOR is set", func(t *testing.T) {
		t.Setenv("NO_COLOR", "1")
		forceTerminal(t, true)
		if got := Clr(ColorGreen, "hi"); got != "hi" {
			t.Errorf("Clr = %q, want unwrapped %q", got, "hi")
		}
	})

	t.Run("wraps text in ANSI codes when color is enabled", func(t *testing.T) {
		if runtime.GOOS == osWindows {
			t.Skip("color is disabled on bare Windows terminals")
		}
		t.Setenv("NO_COLOR", "")
		forceTerminal(t, true)
		got := Clr(ColorGreen, "hi")
		if !strings.Contains(got, "hi") || got == "hi" {
			t.Errorf("Clr = %q, want ANSI-wrapped %q", got, "hi")
		}
		if !strings.HasSuffix(got, ColorReset) {
			t.Errorf("Clr = %q, want trailing reset code", got)
		}
	})
}

// TestNoColor_NonTerminalStdout covers OPS-011(b): `ferrogw version > file` and
// `ferrogw status | grep` used to write ANSI escapes into the payload, because
// NoColor consulted NO_COLOR and the Windows terminal vars but never asked
// whether anything was on the other end. This drives the real check — no seam
// override — against a stdout that is a pipe, which is what a redirect gives.
func TestNoColor_NonTerminalStdout(t *testing.T) {
	if runtime.GOOS == osWindows {
		t.Skip("the Windows branch already suppresses colour without a TTY check")
	}
	t.Setenv("NO_COLOR", "")

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer func() { _ = r.Close() }()
	defer func() { _ = w.Close() }()

	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	if !NoColor() {
		t.Error("NoColor() = false with a piped stdout; colour must be suppressed off a terminal")
	}
	if got := Clr(ColorGreen, "hi"); got != "hi" {
		t.Errorf("Clr = %q, want unwrapped %q when stdout is not a terminal", got, "hi")
	}
}
