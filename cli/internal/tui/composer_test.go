package tui

import (
	"slices"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/spf13/cobra"

	"github.com/ferro-labs/ai-gateway/cli/internal/tui/theme"
)

// fakeRoot mirrors the shape of the real cobra tree — a couple of leaf verbs, a
// parent with children, and the two commands the walk must skip. The real tree
// is asserted against in verbs_cobra_test.go, which lives in the external test
// package so internal/tui never imports internal/command.
func fakeRoot() *cobra.Command {
	root := &cobra.Command{Use: "ferro"}
	keys := &cobra.Command{Use: "keys"}
	keys.AddCommand(&cobra.Command{Use: "create"}, &cobra.Command{Use: "rotate"}, &cobra.Command{Use: "revoke"})
	logs := &cobra.Command{Use: "logs"}
	logs.AddCommand(&cobra.Command{Use: "tail"}, &cobra.Command{Use: "stats"})
	root.AddCommand(
		&cobra.Command{Use: "status"},
		&cobra.Command{Use: "providers"},
		keys, logs,
		&cobra.Command{Use: "completion"},
		&cobra.Command{Use: "help"},
	)
	return root
}

// composerApp is an App whose composer speaks the fake tree's vocabulary.
func composerApp() *App {
	a := testApp(126, 40)
	a.Composer = newComposer(Verbs(fakeRoot()))
	return a
}

// run drives one key and returns the message its command produced, or nil.
func run(t *testing.T, a *App, key string) any {
	t.Helper()
	cmd := a.Composer.handle(key, a)
	if cmd == nil {
		return nil
	}
	return cmd()
}

func typeIn(a *App, s string) {
	for _, r := range s {
		key := string(r)
		if r == ' ' {
			key = "space"
		}
		a.Composer.handle(key, a)
	}
}

func TestComposerEnterEmitsAndRecords(t *testing.T) {
	a := composerApp()
	typeIn(a, "status")

	msg, ok := run(t, a, "enter").(runCmdMsg)
	if !ok || msg.raw != "status" {
		t.Fatalf("enter must emit runCmdMsg{status}, got %#v", msg)
	}
	if a.Composer.Input != "" {
		t.Fatalf("enter must clear the input, got %q", a.Composer.Input)
	}
	if len(a.Composer.History) != 1 || a.Composer.History[0] != "status" {
		t.Fatalf("enter must record history, got %v", a.Composer.History)
	}
}

func TestComposerDoesNotRetainPlaygroundPrompts(t *testing.T) {
	a := composerApp()
	a.Screen = ScreenPlayground
	typeIn(a, "SENTINEL_SECRET_DO_NOT_STORE")

	msg, ok := run(t, a, "enter").(runCmdMsg)
	if !ok || msg.raw != "SENTINEL_SECRET_DO_NOT_STORE" {
		t.Fatalf("the prompt must still be submitted, got %#v", msg)
	}
	if len(a.Composer.History) != 0 {
		t.Fatalf("playground prompts must not enter command history: %v", a.Composer.History)
	}
}

func TestComposerDoesNotRetainUnknownSlashPrompts(t *testing.T) {
	a := composerApp()
	a.Screen = ScreenPlayground
	a.Composer.Input = "/customer-secret please analyze"
	msg, ok := run(t, a, "enter").(runCmdMsg)
	if !ok || msg.raw != "customer-secret please analyze" {
		t.Fatalf("unknown slash input remains a prompt: %#v", msg)
	}
	if len(a.Composer.History) != 0 {
		t.Fatalf("unknown slash prompts can carry private data and must not be retained: %v", a.Composer.History)
	}

	a.Composer.Input = "/model gpt-4o"
	run(t, a, "enter")
	if len(a.Composer.History) != 1 || a.Composer.History[0] != "model gpt-4o" {
		t.Fatalf("known slash actions should remain recallable: %v", a.Composer.History)
	}
}

func TestComposerSanitizesInlineChatHistory(t *testing.T) {
	for _, input := range []string{
		"chat SENTINEL_SECRET_DO_NOT_STORE",
		"CHAT SENTINEL_SECRET_DO_NOT_STORE",
		"PLAYGROUND SENTINEL_SECRET_DO_NOT_STORE",
		"//chat SENTINEL_SECRET_DO_NOT_STORE",
	} {
		t.Run(input, func(t *testing.T) {
			a := composerApp()
			a.Composer.Input = input
			run(t, a, "enter")
			if got := a.Composer.History; len(got) != 1 ||
				(got[0] != "chat" && got[0] != "playground") {
				t.Fatalf("history may retain the verb but not its prompt: %v", got)
			}
			if strings.Contains(strings.Join(a.Composer.History, " "), "SENTINEL") {
				t.Fatalf("history retained prompt content: %v", a.Composer.History)
			}
		})
	}
}

func TestComposerEmptyEnterIsNoop(t *testing.T) {
	a := composerApp()
	typeIn(a, "   ")
	if cmd := a.Composer.handle("enter", a); cmd != nil {
		t.Fatal("a blank line must not run anything")
	}
	if len(a.Composer.History) != 0 {
		t.Fatalf("a blank line must not enter history, got %v", a.Composer.History)
	}
}

func TestComposerTabCompletesPrefix(t *testing.T) {
	a := composerApp()
	typeIn(a, "sta")
	run(t, a, "tab")
	if a.Composer.Input != "status" {
		t.Fatalf("tab must complete to the first matching verb, got %q", a.Composer.Input)
	}

	// A prefix nothing matches leaves the line exactly as typed.
	a.Composer.Input = "zzz"
	run(t, a, "tab")
	if a.Composer.Input != "zzz" {
		t.Fatalf("tab with no match must not rewrite the line, got %q", a.Composer.Input)
	}
}

func TestComposerHistoryWalk(t *testing.T) {
	a := composerApp()
	typeIn(a, "status")
	run(t, a, "enter")
	typeIn(a, "providers")
	run(t, a, "enter")

	// The live input must survive a walk into history and come back intact.
	typeIn(a, "half typed")

	run(t, a, "up")
	if a.Composer.Input != "providers" {
		t.Fatalf("first up must recall the newest command, got %q", a.Composer.Input)
	}
	run(t, a, "up")
	if a.Composer.Input != "status" {
		t.Fatalf("second up must recall the older command, got %q", a.Composer.Input)
	}
	run(t, a, "up")
	if a.Composer.Input != "status" {
		t.Fatalf("up past the oldest entry must stay put, got %q", a.Composer.Input)
	}
	run(t, a, "down")
	if a.Composer.Input != "providers" {
		t.Fatalf("down must walk back toward the live input, got %q", a.Composer.Input)
	}
	run(t, a, "down")
	if a.Composer.Input != "half typed" {
		t.Fatalf("down off the end must restore the live input, got %q", a.Composer.Input)
	}
}

func TestComposerHistoryBounded(t *testing.T) {
	a := composerApp()
	for i := range 35 {
		a.Composer.Input = string(rune('a'+i%26)) + strings.Repeat("x", i)
		run(t, a, "enter")
	}
	if len(a.Composer.History) != historyMax {
		t.Fatalf("history must be bounded at %d, got %d", historyMax, len(a.Composer.History))
	}
	if a.Composer.History[0] != "i"+strings.Repeat("x", 34) {
		t.Fatalf("history must be newest first, got %q", a.Composer.History[0])
	}
}

func TestComposerHistoryDedupesConsecutive(t *testing.T) {
	a := composerApp()
	for range 3 {
		a.Composer.Input = "status"
		run(t, a, "enter")
	}
	a.Composer.Input = "providers"
	run(t, a, "enter")
	a.Composer.Input = "status"
	run(t, a, "enter")

	if got := a.Composer.History; len(got) != 3 {
		t.Fatalf("consecutive duplicates must collapse, got %v", got)
	}
}

func TestComposerReverseSearch(t *testing.T) {
	a := composerApp()
	typeIn(a, "status")
	run(t, a, "enter")
	typeIn(a, "providers")
	run(t, a, "enter")

	run(t, a, "ctrl+r")
	if !a.Composer.searchMode {
		t.Fatal("ctrl+r must enter reverse search")
	}
	typeIn(a, "sta")
	if got := a.Composer.searchMatch(); got != "status" {
		t.Fatalf("search must match history, got %q", got)
	}
	if v := a.Composer.view(a); !strings.Contains(v, "status") {
		t.Fatalf("search mode must show the match:\n%s", v)
	}
	run(t, a, "enter")
	if a.Composer.searchMode || a.Composer.Input != "status" {
		t.Fatalf("enter must accept the match and leave search (mode=%v input=%q)", a.Composer.searchMode, a.Composer.Input)
	}

	// Esc abandons the search without touching the line.
	a.Composer.Input = "kept"
	run(t, a, "ctrl+r")
	typeIn(a, "pro")
	run(t, a, "esc")
	if a.Composer.searchMode || a.Composer.Input != "kept" {
		t.Fatalf("esc must abandon search intact (mode=%v input=%q)", a.Composer.searchMode, a.Composer.Input)
	}
}

func TestComposerSlashAlias(t *testing.T) {
	a := composerApp()
	typeIn(a, "/logs")
	msg, ok := run(t, a, "enter").(runCmdMsg)
	if !ok || msg.raw != "logs" {
		t.Fatalf("a leading / must be stripped, got %#v", msg)
	}
}

func TestComposerQuestionMarkOnEmptyInputAsksForHelp(t *testing.T) {
	a := composerApp()
	msg, ok := run(t, a, "?").(runCmdMsg)
	if !ok || msg.raw != "help" {
		t.Fatalf("? on an empty line must run help, got %#v", msg)
	}
	if a.Composer.Input != "" {
		t.Fatalf("? must not be typed into the line, got %q", a.Composer.Input)
	}

	// With a line in progress it is just a character.
	typeIn(a, "logs ?")
	if a.Composer.Input != "logs ?" {
		t.Fatalf("? mid-line is a character, got %q", a.Composer.Input)
	}
}

func TestComposerBackspaceAndEscape(t *testing.T) {
	a := composerApp()
	typeIn(a, "abc")
	run(t, a, "backspace")
	if a.Composer.Input != "ab" {
		t.Fatalf("backspace must delete one rune, got %q", a.Composer.Input)
	}
	a.Screen = ScreenLogs
	run(t, a, "esc")
	if a.Screen != ScreenHome {
		t.Fatal("esc outside search must return to home")
	}
}

func TestComposerGhostAndSuggestions(t *testing.T) {
	a := composerApp()
	typeIn(a, "k")
	if got := a.Composer.ghost(); got != "keys" {
		t.Fatalf("ghost must name the first matching verb, got %q", got)
	}
	if s := a.Composer.suggestions(); len(s) == 0 || !slices.Contains(s, "keys create") {
		t.Fatalf("suggestions must include the subcommands, got %v", s)
	}
	if s := a.Composer.suggestions(); len(s) > suggestMax {
		t.Fatalf("suggestions are capped at %d, got %d", suggestMax, len(s))
	}
	v := a.Composer.view(a)
	if !strings.Contains(v, "tab") || !strings.Contains(v, "keys") {
		t.Fatalf("the composer must render the ghost hint:\n%s", v)
	}

	a.Composer.Input = ""
	if a.Composer.ghost() != "" || len(a.Composer.suggestions()) != 0 {
		t.Fatal("an empty line offers no ghost and no suggestions")
	}
}

func TestVerbsWalksTheTree(t *testing.T) {
	v := Verbs(fakeRoot())
	for _, want := range []string{"status", "keys", "keys create", "logs tail", "playground", "clear", "help", "/providers"} {
		if !slices.Contains(v, want) {
			t.Fatalf("Verbs must contain %q, got %v", want, v)
		}
	}
	for _, unwanted := range []string{"completion", "/keys create"} {
		if slices.Contains(v, unwanted) {
			t.Fatalf("Verbs must not contain %q, got %v", unwanted, v)
		}
	}
	// A nil tree still yields the TUI-only vocabulary rather than panicking.
	if got := Verbs(nil); !slices.Contains(got, "clear") {
		t.Fatalf("Verbs(nil) must still carry the TUI verbs, got %v", got)
	}
}

// The composer sits inside a frame; a line wider than the terminal wraps and
// destroys every frame above it.
func TestComposerViewFitsEveryWidth(t *testing.T) {
	for _, mode := range []theme.Mode{{}, {Color: true}, {ASCII: true}, {Color: true, ASCII: true}} {
		for _, w := range []int{40, 72, 96, 126} {
			a := composerApp()
			a.Theme, a.Width = theme.New(mode), w
			typeIn(a, "k")
			for _, view := range []string{a.Composer.view(a), a.render()} {
				for i, line := range strings.Split(view, "\n") {
					if lw := lipgloss.Width(line); lw > w {
						t.Fatalf("mode=%+v w=%d line %d overflows: %d cells %q", mode, w, i, lw, line)
					}
				}
			}
		}
	}
}
