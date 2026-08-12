package tui

import (
	"strings"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"
)

// The composer is the console's only navigation surface: there is no menu and
// no key-per-screen. Everything an operator can do here is a verb they could
// also have typed at a shell, which is why the vocabulary is walked out of the
// real cobra tree rather than listed a second time in this file.

const (
	// historyMax is the recall depth. A ring, not a log: the composer is a
	// navigation aid, and a shell already keeps the durable history.
	historyMax = 30

	// suggestMax caps the suggestion row at what fits on one line without
	// pushing the pane around.
	suggestMax = 6

	// cobraCompletion is the shell-completion command cobra generates. The
	// console skips it for the same reason it skips help: it cannot install a
	// script into the shell that launched it.
	cobraCompletion = "completion"
)

// The console's keyboard vocabulary, spelled exactly as tea.KeyPressMsg.String()
// spells it. The composer, the modal and every screen's handleKey are keyed on
// these strings rather than on tea key types — that is what makes them testable
// — so a mistyped literal is a binding that silently never fires and no test
// that drives the same typo can see it. Naming them once turns that into a
// compile error. The test suites deliberately keep typing the raw strings: they
// are the independent check that these values are what the terminal sends.
const (
	keyEnter     = "enter"
	keyEsc       = "esc"
	keyUp        = "up"
	keyDown      = "down"
	keyLeft      = "left"
	keyRight     = "right"
	keyTab       = "tab"
	keySpace     = "space"
	keyBackspace = "backspace"
	keyCtrlC     = "ctrl+c"
	keyCtrlR     = "ctrl+r"
	keyCtrlU     = "ctrl+u"
)

// tuiVerbs exist only in the console — there is no `ferro clear` to script.
var tuiVerbs = []string{verbPlayground, verbModel, verbClear, verbHelp}

// Composer is the command line: input, completion, bounded history, and
// reverse search. It holds no styles; every rendering decision reads the App's
// theme at draw time, so a mode switch needs no rebuild.
type Composer struct {
	Input   string
	History []string // newest first

	verbs []string

	// histIdx is -1 for the live input, which live holds while a walk is in
	// progress. Losing a half-typed line to an accidental ↑ is the one thing
	// history recall must never do.
	histIdx int
	live    string

	searchMode bool
	searchTerm string
}

func newComposer(verbs []string) Composer {
	return Composer{verbs: verbs, histIdx: -1}
}

// Verbs is the console's vocabulary: every path in the cobra tree, plus the
// three verbs that exist only here, plus a slash alias per top-level verb.
//
// It takes the root rather than building one so internal/tui imports no CLI
// package: internal/command is what hands bare `ferro` to Run, and an import
// back the other way would be a cycle.
func Verbs(root *cobra.Command) []string {
	verbs := []string{}
	var walk func(prefix string, cmd *cobra.Command)
	walk = func(prefix string, cmd *cobra.Command) {
		for _, sub := range cmd.Commands() {
			name := sub.Name()
			// help and completion are shell plumbing, not operations: the
			// console prints its own verb tree and cannot install a completion
			// script into the shell that launched it.
			if name == verbHelp || name == cobraCompletion || sub.Hidden {
				continue
			}
			path := strings.TrimSpace(prefix + " " + name)
			verbs = append(verbs, path)
			walk(path, sub)
		}
	}
	if root != nil {
		walk("", root)
	}
	verbs = append(verbs, tuiVerbs...)

	// Slash aliases are top-level only: /keys is an action, "/keys create" is
	// not a spelling anyone types.
	aliases := make([]string, 0, len(verbs))
	for _, v := range verbs {
		if !strings.Contains(v, " ") {
			aliases = append(aliases, "/"+v)
		}
	}
	return append(verbs, aliases...)
}

// handle is keyed on the key's string form, which is what makes the composer
// testable: constructing tea.KeyPressMsg values is awkward and the mapping from
// one to the other is untested glue either way.
func (c *Composer) handle(key string, a *App) tea.Cmd {
	if c.searchMode {
		return c.handleSearch(key)
	}
	switch key {
	case keyEnter:
		return c.submit(a)
	case keyTab:
		if m := c.match(); m != "" {
			c.Input = m
		}
		return nil
	case keyUp:
		c.walk(1)
		return nil
	case keyDown:
		c.walk(-1)
		return nil
	case keyCtrlR:
		c.searchMode, c.searchTerm = true, ""
		return nil
	case keyEsc:
		a.Screen = ScreenHome
		return nil
	case keyBackspace:
		c.Input = trimLastRune(c.Input)
		return nil
	case keyCtrlU:
		c.Input = ""
		return nil
	case keySpace:
		c.Input += " "
		return nil
	case "?":
		// Only on an empty line: mid-line it is a character, and `logs ?` must
		// not silently become the help screen.
		if strings.TrimSpace(c.Input) == "" {
			return emit(runCmdMsg{raw: verbHelp})
		}
	}
	if r, ok := printable(key); ok {
		c.Input += string(r)
	}
	return nil
}

func (c *Composer) handleSearch(key string) tea.Cmd {
	switch key {
	case keyEnter:
		if m := c.searchMatch(); m != "" {
			c.Input = m
		}
		c.searchMode, c.searchTerm = false, ""
		return nil
	case keyEsc, keyCtrlC:
		// Abandoning a search leaves the line exactly as it was found.
		c.searchMode, c.searchTerm = false, ""
		return nil
	case keyBackspace:
		c.searchTerm = trimLastRune(c.searchTerm)
		return nil
	case keySpace:
		c.searchTerm += " "
		return nil
	}
	if r, ok := printable(key); ok {
		c.searchTerm += string(r)
	}
	return nil
}

// submit runs the line. A blank line is a no-op rather than an error: pressing
// enter on an empty composer is how people check that the console is alive.
func (c *Composer) submit(a *App) tea.Cmd {
	input := strings.TrimSpace(c.Input)
	// EVERY leading slash goes, not one. The sanitisation below reads the head
	// of raw to decide what may be retained, so a line that keeps a slash on its
	// head names no verb, matches nothing, and is recorded whole — which for
	// //chat is the prompt, in full, in history and reverse search.
	raw := strings.TrimLeft(input, "/")
	if raw == "" {
		return nil
	}
	// Prompts can contain credentials or customer data. Keep command recall, but
	// never retain prompt content in history or reverse search.
	record := a.Screen != ScreenPlayground
	if a.Screen == ScreenPlayground && strings.HasPrefix(input, "/") {
		fields := strings.Fields(raw)
		record = len(fields) > 0 && knownVerb(c.verbs, strings.ToLower(fields[0]))
	}
	if record {
		history := raw
		fields := strings.Fields(raw)
		if len(fields) > 1 {
			verb := strings.ToLower(fields[0])
			if verb == verbChat || verb == verbPlayground {
				history = verb
			}
		}
		c.record(history)
	}
	c.Input, c.histIdx, c.live = "", -1, ""
	return emit(runCmdMsg{raw: raw})
}

// record pushes onto the ring, newest first. Consecutive duplicates collapse:
// running `status` four times while watching a rollout should not cost four
// entries of recall.
func (c *Composer) record(raw string) {
	if len(c.History) > 0 && c.History[0] == raw {
		return
	}
	next := make([]string, 0, min(len(c.History)+1, historyMax))
	next = append(next, raw)
	next = append(next, c.History...)
	if len(next) > historyMax {
		next = next[:historyMax]
	}
	c.History = next
}

// walk moves d steps toward older history (+1) or back toward the live input
// (-1), stopping at both ends rather than wrapping.
func (c *Composer) walk(d int) {
	i := c.histIdx + d
	if i < -1 || i >= len(c.History) {
		return
	}
	if c.histIdx == -1 {
		c.live = c.Input
	}
	c.histIdx = i
	if i == -1 {
		c.Input = c.live
		return
	}
	c.Input = c.History[i]
}

// query is the completion prefix: trimmed, and with the slash alias folded
// away so /log and log complete identically.
func (c *Composer) query() string {
	return strings.TrimPrefix(strings.TrimSpace(c.Input), "/")
}

// match is the verb tab completes to, and the one the ghost hint names.
func (c *Composer) match() string {
	q := c.query()
	if q == "" {
		return ""
	}
	for _, v := range c.verbs {
		if v != q && strings.HasPrefix(v, q) {
			return v
		}
	}
	return ""
}

// ghost is the first matching verb, or "" when the line already names one.
func (c *Composer) ghost() string { return c.match() }

func (c *Composer) suggestions() []string {
	q := c.query()
	if q == "" {
		return nil
	}
	out := make([]string, 0, suggestMax)
	for _, v := range c.verbs {
		if v == q || !strings.HasPrefix(v, q) {
			continue
		}
		out = append(out, v)
		if len(out) == suggestMax {
			break
		}
	}
	return out
}

func (c *Composer) searchMatch() string {
	if c.searchTerm == "" {
		return ""
	}
	for _, h := range c.History {
		if strings.Contains(h, c.searchTerm) {
			return h
		}
	}
	return ""
}

// view draws the framed command line. Its height is 3 lines, or 5 when there
// are suggestions to show; render() measures it rather than assuming, so the
// pane above gives up the two lines instead of the header scrolling away.
func (c *Composer) view(a *App) string {
	inner := a.Width - 4
	g := a.Theme.Mode.Glyphs()
	prompt := a.Theme.Accent.Bold(a.Theme.Mode.Color).Render(g.Prompt)

	var left, right string
	switch {
	case c.searchMode:
		left = prompt + " " + a.Theme.Dim.Render("search:") + " " +
			a.Theme.Bright.Render(c.searchTerm) + c.cursor(a)
		if m := c.searchMatch(); m != "" {
			right = a.Theme.Dim.Render(a.arrow() + " " + m)
		} else if c.searchTerm != "" {
			right = a.Theme.Dim.Render("no match")
		}
	case c.Input == "":
		left = prompt + " " + c.cursor(a) +
			a.Theme.Dim.Render(" Type a command, / for actions, or ? for help")
	default:
		left = prompt + " " + a.Theme.Bright.Render(c.Input) + c.cursor(a)
		if m := c.ghost(); m != "" {
			right = a.Theme.Faint.Render("tab " + a.arrow() + " " + m)
		}
	}

	lines := []string{gapPad(inner, left, right)}
	if s := c.suggestions(); len(s) > 0 {
		lines = append(lines,
			a.Theme.Hairline.Render(a.rule(max(inner, 0))),
			a.Theme.Dim.Render(padRight(strings.Join(s, "   "), inner)))
	}
	return a.Theme.Frame("COMMAND", strings.Join(lines, "\n"), a.Width)
}

// cursor is drawn rather than left to the terminal: the console renders one
// frame at a time and a hardware cursor would sit wherever the last write did.
func (c *Composer) cursor(a *App) string {
	if a.Theme.Mode.ASCII {
		return a.Theme.Accent.Render("_")
	}
	return a.Theme.Accent.Render("▌")
}

// ------------------------------------------------------------------ helpers

func emit(msg tea.Msg) tea.Cmd { return func() tea.Msg { return msg } }

// arrow is the "leads to" glyph the ghost hint and search preview use.
func (a *App) arrow() string {
	if a.Theme.Mode.ASCII {
		return "->"
	}
	return "→"
}

// printable reports whether a key string is a single printable rune — the
// only keys the composer types into the line. Named keys ("enter", "ctrl+r")
// are multi-rune and fall through.
func printable(key string) (rune, bool) {
	r, size := utf8.DecodeRuneInString(key)
	if size != len(key) || r == utf8.RuneError || !unicode.IsPrint(r) {
		return 0, false
	}
	return r, true
}

func trimLastRune(s string) string {
	if s == "" {
		return ""
	}
	_, size := utf8.DecodeLastRuneInString(s)
	return s[:len(s)-size]
}
