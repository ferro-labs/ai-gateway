package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ferro-labs/ai-gateway/cli/internal/tui/theme"
)

// typeModal types a literal string into the open modal, mapping the space bar
// to the key name the runtime sends. It is the modal's twin of typeIn.
func typeModal(a *App, m *Modal, s string) {
	for _, r := range s {
		key := string(r)
		if r == ' ' {
			key = "space"
		}
		m.handle(key, a)
	}
}

func TestModalEscCancels(t *testing.T) {
	a := composerApp()
	confirmed := false
	a.Modal = &Modal{
		Title:     "Revoke ci-pipeline?",
		Input:     true,
		OnConfirm: func(*App, string) tea.Cmd { confirmed = true; return nil },
	}
	a.update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if a.Modal != nil {
		t.Fatal("esc must close the modal")
	}
	if confirmed {
		t.Fatal("esc must never confirm")
	}
}

func TestModalEscRunsCancelCallback(t *testing.T) {
	a := composerApp()
	cancelled := false
	a.Modal = &Modal{OnCancel: func(*App) tea.Cmd {
		return func() tea.Msg {
			cancelled = true
			return nil
		}
	}}
	cmd := a.Modal.handle("esc", a)
	if cmd == nil {
		t.Fatal("cancel callback command was dropped")
	}
	_ = cmd()
	if !cancelled || a.Modal != nil {
		t.Fatal("esc must close the modal and run OnCancel")
	}
}

func TestModalUsesConfiguredOptionLabel(t *testing.T) {
	a := composerApp()
	a.Modal = &Modal{Options: []string{"one", "two"}, OptionLabel: "mode"}
	if view := a.paneView(80, 12); !strings.Contains(view, "mode") || strings.Contains(view, "scope") {
		t.Fatalf("option label must be generic and caller-defined:\n%s", view)
	}
}

// An open modal owns the keyboard. A typed confirmation that leaks its keys
// into the composer is a confirmation racing a command line.
func TestModalTakesEveryKeyFromTheComposer(t *testing.T) {
	a := composerApp()
	a.Modal = &Modal{Input: true}
	a.update(tea.KeyPressMsg{Code: 'x'})
	if a.Composer.Input != "" {
		t.Fatalf("the composer must not see a key while a modal is open, got %q", a.Composer.Input)
	}
	if a.Modal.Value != "x" {
		t.Fatalf("the modal input must receive the key, got %q", a.Modal.Value)
	}
}

func TestModalTypedConfirmGate(t *testing.T) {
	a := composerApp()
	calls, got := 0, ""
	m := &Modal{
		Title: "Revoke ci-pipeline?", Input: true, Confirm: "ci-pipeline",
		OnConfirm: func(_ *App, v string) tea.Cmd { calls, got = calls+1, v; return nil },
	}
	a.Modal = m

	m.handle("enter", a)
	typeModal(a, m, "ci-pipe")
	m.handle("enter", a)
	if calls != 0 {
		t.Fatalf("a partial phrase must not confirm (%d calls)", calls)
	}
	if a.Modal == nil {
		t.Fatal("a gated modal stays open until the phrase matches")
	}

	typeModal(a, m, "line")
	m.handle("enter", a)
	if calls != 1 || got != "ci-pipeline" {
		t.Fatalf("the exact phrase must confirm once with the typed value, got %d calls %q", calls, got)
	}
	if a.Modal != nil {
		t.Fatal("a confirmed modal closes")
	}
}

func TestModalOptionsToggle(t *testing.T) {
	a := composerApp()
	m := &Modal{Options: []string{"read_only", "admin"}}
	a.Modal = m
	if m.Option() != "read_only" {
		t.Fatalf("the first option is the default, got %q", m.Option())
	}
	m.handle("space", a)
	if m.Option() != "admin" {
		t.Fatalf("space must advance the toggle, got %q", m.Option())
	}
	m.handle("right", a)
	if m.Option() != "read_only" {
		t.Fatalf("the toggle wraps, got %q", m.Option())
	}
	m.handle("left", a)
	if m.Option() != "admin" {
		t.Fatalf("left steps back, got %q", m.Option())
	}
}

// The modal renders in place of the pane, so it owes the pane's contract:
// exactly rows lines, none wider than the frame it sits in.
func TestModalViewKeepsThePaneContract(t *testing.T) {
	for _, mode := range []theme.Mode{{}, {Color: true}, {ASCII: true}, {Color: true, ASCII: true}} {
		a := composerApp()
		a.Theme = theme.New(mode)
		a.Modal = &Modal{
			Title:       "Create API key · name",
			Body:        "Names are how log rows resolve after a key is revoked.",
			Rows:        []ModalKV{{K: "name", V: "ci-readonly", Bold: true}, {K: "scope", V: "read_only", Accent: true}},
			Input:       true,
			Placeholder: "prod-ingest",
			OKLabel:     "next",
			Danger:      true,
		}
		for _, rows := range []int{1, 3, 6, 14, 22} {
			for _, w := range []int{30, 60, 92, 126} {
				body := a.paneView(w, rows)
				if got := len(strings.Split(body, "\n")); got != rows {
					t.Fatalf("mode=%+v w=%d rows=%d: modal returned %d lines", mode, w, rows, got)
				}
				for i, line := range strings.Split(body, "\n") {
					if lw := lipgloss.Width(line); lw > w {
						t.Fatalf("mode=%+v w=%d line %d overflows: %d cells %q", mode, w, i, lw, line)
					}
				}
			}
		}
	}
}

// A modal body can carry intentional line breaks; rendering must preserve them
// instead of collapsing the wording into one run-on line.
func TestModalRendersEveryBodyLine(t *testing.T) {
	a := composerApp()
	a.Modal = &Modal{
		Title:   "Rotate ops-laptop?",
		Body:    "Blast radius: every client holding the current secret fails immediately\nuntil it is redeployed with the new one.",
		OKLabel: "rotate",
	}
	view := a.paneView(110, 16)
	for _, want := range []string{"Blast radius", "until it is redeployed with the new one."} {
		if !strings.Contains(view, want) {
			t.Fatalf("modal body lost %q:\n%s", want, view)
		}
	}
}
