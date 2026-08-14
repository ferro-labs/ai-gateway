package theme

import (
	"strings"
	"testing"
	"unicode"

	"charm.land/lipgloss/v2"
)

func TestFrameClosesAtExactWidth(t *testing.T) {
	th := New(Mode{}) // no color → deterministic output
	got := th.Frame("LOGS", "hello", 20)
	want := "" +
		"┌─ LOGS ───────────┐\n" +
		"│ hello            │\n" +
		"└──────────────────┘"
	if got != want {
		t.Fatalf("frame drift:\n%s\nwant:\n%s", got, want)
	}
	for i, line := range strings.Split(got, "\n") {
		if w := lipgloss.Width(line); w != 20 {
			t.Fatalf("line %d width %d != 20 (cell measurement)", i, w)
		}
	}
}

// Frame is pinned above in the default unicode box only, and that is one of
// three border sets: Mode.box answers differently under --ascii, and FrameRound
// is what the composer and every modal draw. All three do the same
// cell-accurate padding, so all three are measured.
func TestFrameVariantsCloseAtExactWidth(t *testing.T) {
	const w = 20
	for _, m := range []Mode{{}, {ASCII: true}} {
		th := New(m)
		for _, got := range []string{th.Frame("LOGS", "hello", w), th.FrameRound("hello", w)} {
			for i, line := range strings.Split(got, "\n") {
				if got := lipgloss.Width(line); got != w {
					t.Fatalf("mode=%+v line %d width %d != %d: %q", m, i, got, w, line)
				}
			}
		}
	}
}

func TestFrameUnicodeContentDoesNotBreakBorders(t *testing.T) {
	th := New(Mode{})
	got := th.Frame("T", "métriques ✓ 日本語", 26)
	for _, line := range strings.Split(got, "\n") {
		if lipgloss.Width(line) != 26 {
			t.Fatalf("unicode content broke cell math: %q", line)
		}
	}
}

func TestFrameTooNarrowReturnsBody(t *testing.T) {
	th := New(Mode{})
	if got := th.Frame("T", "x", 6); got != "x" {
		t.Fatalf("narrow frame must degrade to bare body, got %q", got)
	}
}

func TestGlyphsASCII(t *testing.T) {
	g := Mode{ASCII: true}.Glyphs()
	if g.OK != "[OK]" || g.Bad != "[X]" || g.Warn != "[!]" || g.None != "[-]" || g.Prompt != ">" {
		t.Fatalf("ascii glyph vocabulary drifted: %+v", g)
	}
}

func TestMarkShape(t *testing.T) {
	if len(MarkRows) != 7 {
		t.Fatal("mark is exactly 7 rows — never redraw it")
	}
}

func TestMarkIsASCIIInASCIIMode(t *testing.T) {
	for _, r := range Mark(New(Mode{ASCII: true})) {
		if r > unicode.MaxASCII {
			t.Fatalf("ASCII mark contains %q", r)
		}
	}
}
