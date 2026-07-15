package cli

import (
	"runtime"
	"strings"
	"testing"
)

func TestClr(t *testing.T) {
	t.Run("passes text through when NO_COLOR is set", func(t *testing.T) {
		t.Setenv("NO_COLOR", "1")
		if got := Clr(ColorGreen, "hi"); got != "hi" {
			t.Errorf("Clr = %q, want unwrapped %q", got, "hi")
		}
	})

	t.Run("wraps text in ANSI codes when color is enabled", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("color is disabled on bare Windows terminals")
		}
		t.Setenv("NO_COLOR", "")
		got := Clr(ColorGreen, "hi")
		if !strings.Contains(got, "hi") || got == "hi" {
			t.Errorf("Clr = %q, want ANSI-wrapped %q", got, "hi")
		}
		if !strings.HasSuffix(got, ColorReset) {
			t.Errorf("Clr = %q, want trailing reset code", got)
		}
	})
}
