package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/ferro-labs/ai-gateway/cli/internal/tui/theme"
)

// transcriptMax is the ring depth. Bounded because a console that runs for a
// week must not grow without limit, and 60 rows is what the tallest pane can
// scroll through without paging.
const transcriptMax = 60

// The row kinds. A kind is resolved by glyphCell into a styled glyph, and an
// unrecognised one is rendered as written — which means a mistyped literal
// draws itself into the transcript instead of failing, so the vocabulary is
// named here and every caller spells it from this list. The empty kind ("") is
// a row with no glyph and needs no name.
const (
	kindCmd  = "$" // an echoed command line
	kindOK   = "ok"
	kindWarn = "warn"
	kindBad  = "bad"
	kindNote = "·"
)

// TranscriptRow is one line of command output. Glyph is a kind, not a
// character: the theme resolves it, so the same row reads correctly under
// --ascii and under NO_COLOR.
type TranscriptRow struct {
	Glyph string
	Text  string
	Dim   bool
	Bold  bool
}

// Transcript is the home screen's bounded output history.
//
// dropped is the honest half of the ring: when rows fall off the top the view
// says so. Silent truncation reads as "this is everything", which is exactly
// the wrong thing to believe while reading a gateway's output.
type Transcript struct {
	rows    []TranscriptRow
	dropped bool
}

// Push appends rows, dropping the oldest past the ring bound and recording
// that it did so, so the view can say output was lost.
func (tr *Transcript) Push(rows ...TranscriptRow) {
	tr.rows = append(tr.rows, rows...)
	if n := len(tr.rows) - transcriptMax; n > 0 {
		tr.rows = append(tr.rows[:0], tr.rows[n:]...)
		tr.dropped = true
	}
}

// Reset empties the ring, drop marker included: a cleared transcript has
// dropped nothing.
func (tr *Transcript) Reset() { *tr = Transcript{} }

// Len reports how many rows are currently retained.
func (tr *Transcript) Len() int { return len(tr.rows) }

// view renders exactly h lines at most w cells wide — the pane's contract, and
// what keeps the composer anchored to the bottom of the screen.
func (tr *Transcript) view(th theme.Theme, w, h int) string {
	if h <= 0 {
		return ""
	}
	rows, clipped := tr.rows, tr.dropped
	if len(rows) > h {
		clipped = true
	}
	if clipped {
		// The marker costs a line, so the window it announces is one shorter.
		if len(rows) > h-1 {
			rows = rows[len(rows)-max(h-1, 0):]
		}
	}

	lines := make([]string, 0, h)
	if clipped {
		lines = append(lines, dropMarker(th, w))
	}
	gw := glyphWidth(th)
	for _, r := range rows {
		lines = append(lines, r.render(th, w, gw))
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines[:h], "\n")
}

func (r TranscriptRow) render(th theme.Theme, w, gw int) string {
	text := th.Text
	switch r.Glyph {
	case kindBad:
		text = th.Bad
	case kindWarn:
		text = th.Warn
	}
	if r.Dim {
		text = th.Dim
	}
	if r.Bold {
		text = text.Bold(th.Mode.Color)
	}
	line := padRight(glyphCell(th, r.Glyph), gw) + " " + text.Render(r.Text)
	// A row wider than the pane is cut, never wrapped — wrapping would break
	// the pane's line count and every frame below it. The marker is what keeps
	// the cut from reading as the end of the row.
	return ansi.Truncate(line, w, cutMark(th))
}

func cutMark(th theme.Theme) string {
	if th.Mode.ASCII {
		return ">"
	}
	return "…"
}

// glyphCell resolves a row's kind into a styled glyph. An unrecognised kind is
// rendered as written, which is how kindCmd and any future literal marker work.
func glyphCell(th theme.Theme, kind string) string {
	g := th.Mode.Glyphs()
	switch kind {
	case "":
		return ""
	case kindOK:
		return th.OK.Render(g.OK)
	case kindWarn:
		return th.Warn.Render(g.Warn)
	case kindBad:
		return th.Bad.Render(g.Bad)
	case kindNote:
		return th.Dim.Render(g.None)
	default:
		return th.Dim.Render(kind)
	}
}

// glyphWidth is the glyph column: one cell for the unicode set, four for the
// ASCII set's widest member ("[OK]"). Measured, never assumed — and measured
// over EVERYTHING glyphCell can put in the column, which is the four mapped
// glyphs plus the literal kinds it renders as written. A set that widens a
// glyph nothing measured shifts every row's text by the difference.
func glyphWidth(th theme.Theme) int {
	g := th.Mode.Glyphs()
	return max(
		lipgloss.Width(g.OK), lipgloss.Width(g.Bad),
		lipgloss.Width(g.Warn), lipgloss.Width(g.None),
		lipgloss.Width(kindCmd), 1)
}

func dropMarker(th theme.Theme, w int) string {
	label := " older output dropped "
	rule := "─"
	if th.Mode.ASCII {
		rule = "-"
	}
	fill := max((w-lipgloss.Width(label))/2, 1)
	side := strings.Repeat(rule, fill)
	return ansi.Truncate(th.Faint.Render(side)+th.Dim.Render(label)+th.Faint.Render(side), w, "")
}
