package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ferro-labs/ai-gateway/cli/internal/tui/theme"
)

// This file is the console's one overlay. It renders in place of the pane
// rather than over it: a terminal has no transparency to dim with, and a modal
// drawn on top of live content is a modal whose scrim is a lie about what is
// underneath it.
//
// Two things here are load-bearing rather than cosmetic:
//
//   - Confirm gates the OK arm on typing an exact phrase. A destructive
//     operation confirmed by one keypress is not confirmed, it is acknowledged.
//   - Nothing in this file writes to the transcript or the composer history.
//     A secret shown in a modal is shown here and nowhere else, which is what
//     makes "shown exactly once" true rather than aspirational.

// modalKeyCol is the label column of a key/value row, wide enough for the
// longest label the flows use ("blast radius").
const modalKeyCol = 14

// ModalKV is one key/value row of a modal card. The three flags are the only
// emphasis a modal can apply — a row is never styled by matching on its text.
type ModalKV struct {
	K, V   string
	Accent bool
	Warn   bool
	Bold   bool
}

// Modal is a titled card that owns the keyboard while it is open.
//
// A modal has at most one editing surface: Input (a one-line editor) or
// Options (a left/right toggle). OnConfirm receives the typed value, which for
// a gated modal is exactly Confirm — the caller does not have to re-check it.
type Modal struct {
	Title, Body string
	Rows        []ModalKV

	Input       bool
	Placeholder string
	Value       string

	Options []string
	Opt     int
	// OptionLabel names the toggle row. Empty falls back to "option".
	OptionLabel string

	// Err is a refusal to accept what was typed — a duration that does not
	// parse. It renders under the input rather than replacing the body, so the
	// instruction that was misread stays on screen next to the mistake.
	Err string

	OKLabel string
	Danger  bool

	// Confirm is the phrase Value must equal before OK is enabled. Empty means
	// enter confirms immediately.
	Confirm string

	OnConfirm func(a *App, value string) tea.Cmd
	// OnCancel runs on esc. A flow that must finish something on the way out —
	// recording that a secret was shown and closed, refreshing the list behind
	// it — hangs it here, because esc is a real way to leave and not an error
	// path.
	OnCancel func(a *App) tea.Cmd
}

// Option is the currently selected toggle value, or "" when there is no toggle.
func (m *Modal) Option() string {
	if len(m.Options) == 0 {
		return ""
	}
	return m.Options[m.Opt%len(m.Options)]
}

// collected is what this modal gathered: the selected option for a toggle, the
// typed value otherwise. OnConfirm reads one field rather than branching on
// which editing surface it was given.
func (m *Modal) collected() string {
	if len(m.Options) > 0 {
		return m.Option()
	}
	return strings.TrimSpace(m.Value)
}

// ready reports whether enter may confirm.
func (m *Modal) ready() bool {
	return m.Confirm == "" || strings.TrimSpace(m.Value) == m.Confirm
}

// handle is keyed on the key's string form, exactly as the composer is: the
// mapping from a tea.KeyPressMsg to that string is untested glue either way,
// and keeping it out of here is what makes a confirmation flow testable.
func (m *Modal) handle(key string, a *App) tea.Cmd {
	switch key {
	case keyEsc:
		a.Modal = nil
		if m.OnCancel != nil {
			return m.OnCancel(a)
		}
		return nil

	case keyEnter:
		if !m.ready() {
			return nil
		}
		a.Modal = nil
		if m.OnConfirm != nil {
			return m.OnConfirm(a, m.collected())
		}
		return nil

	case keyLeft:
		m.cycle(-1)
		return nil

	case keyRight:
		m.cycle(1)
		return nil

	case keySpace:
		if len(m.Options) > 0 {
			m.cycle(1)
			return nil
		}
		if m.Input {
			m.Value += " "
		}
		return nil

	case keyBackspace:
		if m.Input {
			m.Value, m.Err = trimLastRune(m.Value), ""
		}
		return nil

	case keyCtrlU:
		if m.Input {
			m.Value, m.Err = "", ""
		}
		return nil
	}

	if r, ok := printable(key); ok && m.Input {
		m.Value, m.Err = m.Value+string(r), ""
	}
	return nil
}

func (m *Modal) cycle(d int) {
	if n := len(m.Options); n > 0 {
		m.Opt = ((m.Opt+d)%n + n) % n
	}
}

// view draws the card into the pane's exact-line contract: rows lines, none
// wider than w. Below the width or height a border can close in, the card
// degrades to its bare content rather than emitting a frame with a hole in it.
func (m *Modal) view(a *App, w, rows int) string {
	if rows < 4 || w < 14 {
		return fill(m.lines(a, w), w, rows)
	}
	return a.Theme.FrameRound(fill(m.lines(a, w-4), w-4, rows-2), w)
}

// lines is the card's content, in order: title, body, rows, editor, actions.
// Blank separators are dropped as the pane gets shorter, so a 4-line modal
// still shows its title and its OK arm.
func (m *Modal) lines(a *App, w int) []string {
	th := a.Theme
	out := []string{th.Bright.Bold(th.Mode.Color).Render(m.Title)}
	for _, line := range strings.Split(m.Body, "\n") {
		if line == "" && m.Body == "" {
			continue
		}
		out = append(out, th.Dim.Render(line))
	}

	if len(m.Rows) > 0 {
		out = append(out, "")
		for _, r := range m.Rows {
			out = append(out, th.Dim.Render(padRight(r.K, modalKeyCol))+kvStyle(th, r).Render(r.V))
		}
	}

	switch {
	case m.Input:
		g := th.Mode.Glyphs()
		field := th.Accent.Bold(th.Mode.Color).Render(g.Prompt) + " "
		if m.Value == "" {
			field += a.Composer.cursor(a) + " " + th.Dim.Render(m.Placeholder)
		} else {
			field += th.Bright.Render(m.Value) + a.Composer.cursor(a)
		}
		out = append(out, "", field)
	case len(m.Options) > 0:
		label := m.OptionLabel
		if label == "" {
			label = "option"
		}
		out = append(out, "", th.Dim.Render(padRight(label, modalKeyCol))+m.options(th))
	}

	if m.Err != "" {
		out = append(out, th.Bad.Render(m.Err))
	}
	return append(out, "", m.actions(a, w))
}

// kvStyle resolves a key/value row's emphasis. Shared with the detail strips,
// which draw the same row shape outside a modal.
func kvStyle(th theme.Theme, r ModalKV) lipgloss.Style {
	s := th.Text
	switch {
	case r.Accent:
		s = th.Accent
	case r.Warn:
		s = th.Warn
	case r.Bold:
		s = th.Bright
	}
	if r.Bold {
		s = s.Bold(th.Mode.Color)
	}
	return s
}

// options draws the toggle. The selected value carries the accent AND the
// brackets: colour never carries state alone, here or anywhere else.
func (m *Modal) options(th theme.Theme) string {
	parts := make([]string, 0, len(m.Options))
	for i, o := range m.Options {
		if i == m.Opt {
			parts = append(parts, th.Accent.Bold(th.Mode.Color).Render("["+o+"]"))
			continue
		}
		parts = append(parts, th.Dim.Render(" "+o+" "))
	}
	return strings.Join(parts, " ") + th.Faint.Render("   space toggles")
}

// actions is the footer: cancel on the left, OK on the right. A gated OK that
// is not yet armed renders faint and says what is missing, so the arm reads as
// disabled rather than broken.
func (m *Modal) actions(a *App, w int) string {
	th := a.Theme
	label := m.OKLabel
	if label == "" {
		label = "ok"
	}
	if len(m.Options) > 0 {
		label = m.Option() + " " + a.arrow() + " " + label
	}

	style := th.Accent
	if m.Danger {
		style = th.Bad
	}
	right := style.Bold(th.Mode.Color).Render("[ "+label+" ]") + th.Dim.Render(" ↵")
	if a.Theme.Mode.ASCII {
		right = style.Bold(th.Mode.Color).Render("[ "+label+" ]") + th.Dim.Render(" enter")
	}
	if !m.ready() {
		right = th.Faint.Render("[ " + label + " ]  type the name to enable")
	}
	return gapPad(w, th.Dim.Render("esc cancel"), right)
}
