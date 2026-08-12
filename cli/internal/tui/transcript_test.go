package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/ferro-labs/ai-gateway/cli/internal/tui/theme"
)

func TestTranscriptRingAndDropMarker(t *testing.T) {
	var tr Transcript
	for i := range 70 {
		tr.Push(TranscriptRow{Text: "row " + string(rune('a'+i%26))})
	}
	if tr.Len() != transcriptMax {
		t.Fatalf("the transcript is a %d-row ring, got %d", transcriptMax, tr.Len())
	}
	v := tr.view(theme.New(theme.Mode{}), 60, 80)
	if !strings.Contains(v, "older output dropped") {
		t.Fatalf("dropped rows must be announced, not silently swallowed:\n%s", v)
	}
}

// A window narrower than the transcript hides rows too, and that is the same
// lie: what is on screen is not everything.
func TestTranscriptMarksAWindowThatClips(t *testing.T) {
	var tr Transcript
	for i := range 10 {
		tr.Push(TranscriptRow{Text: "row " + string(rune('a'+i))})
	}
	th := theme.New(theme.Mode{})
	if v := tr.view(th, 60, 10); strings.Contains(v, "older output dropped") {
		t.Fatalf("a window that shows everything must not claim otherwise:\n%s", v)
	}
	v := tr.view(th, 60, 4)
	if !strings.Contains(v, "older output dropped") {
		t.Fatalf("a clipped window must say so:\n%s", v)
	}
	if !strings.Contains(v, "row j") {
		t.Fatalf("a clipped window keeps the newest rows:\n%s", v)
	}
}

// The pane's height is the shell's anchor for the composer: exactly h lines,
// whatever the transcript holds.
func TestTranscriptViewIsExactlyHLines(t *testing.T) {
	th := theme.New(theme.Mode{})
	for _, rows := range []int{0, 3, 40} {
		var tr Transcript
		for i := range rows {
			tr.Push(TranscriptRow{Text: strings.Repeat("wide ", i)})
		}
		for _, h := range []int{1, 2, 8, 20} {
			if got := len(strings.Split(tr.view(th, 50, h), "\n")); got != h {
				t.Fatalf("view(%d rows, h=%d) returned %d lines", rows, h, got)
			}
		}
	}
	if tr := (Transcript{}); tr.view(th, 50, 0) != "" {
		t.Fatal("a zero-height pane renders nothing")
	}
}

func TestTranscriptGlyphColumnAndWidth(t *testing.T) {
	for _, mode := range []theme.Mode{{}, {Color: true}, {ASCII: true}, {Color: true, ASCII: true}} {
		th := theme.New(mode)
		g := mode.Glyphs()
		var tr Transcript
		tr.Push(
			TranscriptRow{Glyph: "$", Text: "ferro status"},
			TranscriptRow{Glyph: "ok", Text: "gateway ready"},
			TranscriptRow{Glyph: "bad", Text: "unknown command"},
			TranscriptRow{Glyph: "warn", Text: "degraded"},
			TranscriptRow{Text: strings.Repeat("long ", 40), Dim: true},
		)
		v := tr.view(th, 40, 6)
		for _, want := range []string{"$", g.OK, g.Bad, g.Warn, cutMark(th)} {
			if !strings.Contains(v, want) {
				t.Fatalf("mode=%+v glyph %q missing:\n%s", mode, want, v)
			}
		}
		for i, line := range strings.Split(v, "\n") {
			if lw := lipgloss.Width(line); lw > 40 {
				t.Fatalf("mode=%+v line %d overflows: %d cells %q", mode, i, lw, line)
			}
		}
	}
}

func TestTranscriptResetClearsTheDropMarker(t *testing.T) {
	var tr Transcript
	for range 70 {
		tr.Push(TranscriptRow{Text: "x"})
	}
	tr.Reset()
	if tr.Len() != 0 {
		t.Fatalf("reset must empty the ring, got %d", tr.Len())
	}
	if v := tr.view(theme.New(theme.Mode{}), 40, 5); strings.Contains(v, "older output dropped") {
		t.Fatalf("a cleared transcript has dropped nothing:\n%s", v)
	}
}
