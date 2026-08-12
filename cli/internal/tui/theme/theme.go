// Package theme is the ferro console's visual vocabulary: palette, glyph set,
// cell-accurate frames, and the split-F mark.
//
// Every measurement here is in terminal cells (lipgloss.Width), never bytes and
// never runes: CJK is double-width, combining marks are zero-width, and ANSI
// sequences measure zero. A frame padded with len() breaks the first time real
// output arrives.
package theme

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Console palette. Truecolor hex; lipgloss downgrades to the
// terminal's actual profile. Never paint a full-screen background — the
// terminal's own background is the ground.
const (
	colorAccent    = "#d97757"
	colorOK        = "#77ba8d"
	colorWarn      = "#e4b354"
	colorBad       = "#dc6b67"
	colorDim       = "#7f817e"
	colorText      = "#e8e5df"
	colorBright    = "#fff8ef"
	colorFaint     = "#4a4b49"
	colorBorder    = "#3a3b38"
	colorHairline  = "#262726"
	colorSelection = "#1a1717"
)

// minFrameWidth is the narrowest frame that can still close around content:
// two border cells, two padding cells, and something in between.
const minFrameWidth = 8

// Mode is what the terminal will accept. Color is false under NO_COLOR, a
// non-TTY stdout, or TERM=dumb; ASCII is the --ascii glyph downgrade.
type Mode struct {
	Color bool
	ASCII bool
}

// Glyphs is the whole status vocabulary. Color never carries state alone, so
// every state has a glyph here.
type Glyphs struct{ Dot, OK, Warn, Bad, None, Prompt string }

// Glyphs returns the unicode set, or the ASCII set under --ascii.
func (m Mode) Glyphs() Glyphs {
	if m.ASCII {
		return Glyphs{Dot: "[+]", OK: "[OK]", Warn: "[!]", Bad: "[X]", None: "[-]", Prompt: ">"}
	}
	return Glyphs{Dot: "●", OK: "✓", Warn: "!", Bad: "✗", None: "·", Prompt: "❯"}
}

// box is the border vocabulary for one frame style.
type box struct{ tl, tr, bl, br, h, v string }

func (m Mode) box(round bool) box {
	switch {
	case m.ASCII:
		return box{tl: "+", tr: "+", bl: "+", br: "+", h: "-", v: "|"}
	case round:
		return box{tl: "╭", tr: "╮", bl: "╰", br: "╯", h: "─", v: "│"}
	default:
		return box{tl: "┌", tr: "┐", bl: "└", br: "┘", h: "─", v: "│"}
	}
}

// Theme is the rendering surface every TUI file draws through. When
// Mode.Color is false every style is a plain no-op, so callers never branch on
// color themselves.
type Theme struct {
	Mode                                            Mode
	Accent, OK, Warn, Bad, Dim, Text, Bright, Faint lipgloss.Style
	Border, Hairline                                lipgloss.Style
	Selected                                        lipgloss.Style
}

// New builds the theme for a mode. Under !m.Color every field is
// lipgloss.NewStyle(), which renders its input unchanged.
func New(m Mode) Theme {
	fg := func(hex string) lipgloss.Style {
		s := lipgloss.NewStyle()
		if !m.Color {
			return s
		}
		return s.Foreground(lipgloss.Color(hex))
	}
	t := Theme{
		Mode:     m,
		Accent:   fg(colorAccent),
		OK:       fg(colorOK),
		Warn:     fg(colorWarn),
		Bad:      fg(colorBad),
		Dim:      fg(colorDim),
		Text:     fg(colorText),
		Bright:   fg(colorBright),
		Faint:    fg(colorFaint),
		Border:   fg(colorBorder),
		Hairline: fg(colorHairline),
		Selected: lipgloss.NewStyle(),
	}
	if m.Color {
		t.Selected = t.Selected.Background(lipgloss.Color(colorSelection))
	}
	return t
}

// Frame draws a square-corner frame exactly w cells wide, with the title
// embedded in the top border:
//
//	┌─ TITLE ────────┐
//	│ body           │
//	└────────────────┘
//
// Body lines are padded or truncated to fit. An empty title yields an
// unbroken top border. w < minFrameWidth returns the bare body: never emit a
// border that cannot close.
func (t Theme) Frame(title, body string, w int) string {
	return t.frame(title, body, w, t.Mode.box(false))
}

// FrameRound is Frame with rounded corners and no title — the composer and
// modals.
func (t Theme) FrameRound(body string, w int) string {
	return t.frame("", body, w, t.Mode.box(true))
}

func (t Theme) frame(title, body string, w int, b box) string {
	if w < minFrameWidth {
		return body
	}
	lines := strings.Split(body, "\n")
	rows := make([]string, 0, len(lines)+2)
	rows = append(rows, t.top(b, title, w))
	edge := t.Border.Render(b.v)
	for _, line := range lines {
		rows = append(rows, edge+" "+padCell(line, w-4)+" "+edge)
	}
	rows = append(rows, t.Border.Render(b.bl+strings.Repeat(b.h, w-2)+b.br))
	return strings.Join(rows, "\n")
}

func (t Theme) top(b box, title string, w int) string {
	if title == "" {
		return t.Border.Render(b.tl + strings.Repeat(b.h, w-2) + b.tr)
	}
	// Cap the title so at least one trailing rule remains; w >= minFrameWidth
	// leaves at least two cells for it.
	title = ansi.Truncate(title, w-6, "")
	fill := max(w-5-lipgloss.Width(title), 1)
	return t.Border.Render(b.tl+b.h) + " " + t.Dim.Render(title) + " " +
		t.Border.Render(strings.Repeat(b.h, fill)+b.tr)
}

// padCell pads or truncates s to exactly w terminal cells. Truncation is
// ANSI- and grapheme-aware, so styled content keeps its escape sequences
// intact and a clipped double-width rune still leaves the line whole.
func padCell(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = ansi.Truncate(s, w, "")
	if pad := w - lipgloss.Width(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

// markStem is the F's stem: the three rows that carry no arm are the same row.
const markStem = "     ███"

// MarkRows is the split-F, sampled from f_logo.svg — canonical, do not redraw.
var MarkRows = []string{
	"       ████████",
	"     ██████████",
	markStem,
	"       ██████",
	"     ████████",
	markStem,
	markStem,
}

// Mark renders the split-F in the accent color.
func Mark(t Theme) string {
	if t.Mode.ASCII {
		return t.Accent.Render("F")
	}
	rows := make([]string, len(MarkRows))
	for i, r := range MarkRows {
		rows[i] = t.Accent.Render(r)
	}
	return strings.Join(rows, "\n")
}
