package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/ferro-labs/ai-gateway/cli/internal/api"
)

// The playground is a streamed chat with the route metadata a gateway operator
// actually needs beside the answer. Three things here are not obvious:
//
//   - Deltas are BATCHED. Each one appends to a buffer and a tick moves the
//     buffer into the turn every flushEvery, which is where the single markdown
//     re-render happens. Rendering per token turns a 400-token answer into 400
//     full re-layouts and the terminal into a slideshow.
//   - A streamed body names no provider and carries no price. Both come from
//     the request log, matched on the response's X-Request-ID — so the meta
//     line is assembled from two sources and every segment it cannot support is
//     dropped. An invented provider is worse than a shorter line.
//   - Answers are rendered as predictable, word-wrapped plain text. The
//     renderer remains replaceable without changing the stream state machine.

const (
	// flushEvery is the re-render cadence. Fast enough to read as streaming,
	// slow enough that one render covers many tokens.
	flushEvery = 80 * time.Millisecond

	// attributionRetry is the single re-ask. The log row is written after the
	// stream ends, so the first look can legitimately be too early.
	attributionRetry = 500 * time.Millisecond

	// attributionSlack widens the log window backwards from the moment the
	// request started. created_at is stamped with the GATEWAY's clock and the
	// cursor with ours, so a gateway running even slightly behind writes a row
	// that looks older than the request that produced it — and attribution then
	// fails permanently and silently for that deployment. The window is only a
	// prefilter: the row is still matched on its exact trace id, which is
	// unique, so widening it cannot select the wrong request.
	attributionSlack = 2 * time.Minute

	// modelSuggestions is how many near misses a rejected /model names.
	modelSuggestions = 5

	// playTurnMax bounds both rendered transcript work and the conversation
	// retained in memory. It is intentionally a turn count, not a byte count.
	playTurnMax = 60
)

// The two sides of the conversation. Who is compared against these all over
// this file — to find the turn being written into, to decide a label's style,
// to map a turn onto an API role — so the strings are named rather than
// repeated: a misspelt one reads as a third speaker nothing ever matches.
const (
	whoYou     = "you"
	whoGateway = "gateway"
)

// chatTurn is one side of the conversation. lines is the rendered body, cached
// at flush time: the view is called once per message and must not re-run a
// markdown pass to answer.
type chatTurn struct {
	Who  string // whoYou | whoGateway
	Text string
	Meta string
	Bad  bool
	Note bool

	// id names this turn for as long as it exists. Attribution arrives after
	// the turn it describes has stopped being the last one, and appendTurns
	// trims from the head, so a position is not a name.
	id int

	lines []string
}

// playScreen is the chat pane.
type playScreen struct {
	turns   []chatTurn
	lastID  int // last id handed out by appendTurns
	pending strings.Builder

	// queued holds a `chat <prompt>` prompt while model discovery is in
	// flight. submit snapshots s.model into the request, so submitting before
	// the first playModelsMsg lands sends an empty model.
	queued string

	stream     *api.ChatStream
	openCancel context.CancelFunc
	requestID  string
	started    time.Time
	took       time.Duration
	usage      *api.Usage
	busy       bool

	model  string
	models []string

	width int
	// renderer lays an answer out. It is a field so another renderer can be
	// introduced independently, and so tests can count layout passes.
	renderer func(text string, w int) string

	gen int
}

// ------------------------------------------------------------------ lifecycle

func (s *playScreen) enter(a *App, rest string) tea.Cmd {
	if a.Screen != ScreenPlayground {
		s.gen++
	}
	a.Screen = ScreenPlayground
	s.resize(a.paneWidth())

	var cmds []tea.Cmd
	discovering := len(s.models) == 0 && a.Client != nil
	if discovering {
		cmds = append(cmds, fetchChatModels(a.Client, s.gen))
	}
	// `chat <prompt>` is one line that opens the pane and asks: the scriptable
	// verb takes a prompt, so the console's twin of it must too.
	//
	// It waits for discovery when discovery is running. submit copies s.model
	// into the request there and then, and until playModelsMsg picks a default
	// that field is empty — so the first console chat would be sent naming no
	// model at all and refused by a gateway that would have served it.
	if rest != "" {
		if discovering {
			s.queued = rest
		} else {
			cmds = append(cmds, s.submit(a, rest))
		}
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// leave cancels the stream and invalidates the visit. Cancel is what releases
// the reader goroutine and the connection, and it is safe to call more than
// once — so it is called here whether or not the turn finished cleanly.
func (s *playScreen) leave() {
	s.gen++
	s.cancel()
	s.busy = false
	s.queued = ""
	s.pending.Reset()
}

func (s *playScreen) cancel() {
	if s.openCancel != nil {
		s.openCancel()
		s.openCancel = nil
	}
	if s.stream != nil {
		s.stream.Cancel()
		s.stream = nil
	}
}

// resize re-wraps every turn. The markdown pass runs off the render path, so a
// width change is the only other thing that can invalidate it.
func (s *playScreen) resize(w int) {
	if w <= 0 || w == s.width {
		return
	}
	s.width = w
	if s.renderer == nil {
		return
	}
	for i := range s.turns {
		s.turns[i].lines = s.wrap(s.turns[i].Text)
	}
}

// --------------------------------------------------------------------- input

// route is the playground's half of the command/prompt decision.
//
// It reports whether the playground claimed the line. The check cannot key on a
// leading slash: the composer strips it before route() is reached, so `/status`
// and `status` are the same string by then. What is asked instead is whether the
// head names a verb — the console's vocabulary is the cobra tree's, so "does
// this name something ferro can run" is a question with an exact answer.
func (s *playScreen) route(a *App, head, rest, line string) (tea.Cmd, bool) {
	switch head {
	case verbModel:
		s.setModel(a, rest)
		return nil, true
	case verbClear:
		s.reset()
		return nil, true
	}
	if knownVerb(a.Composer.verbs, head) {
		return nil, false
	}
	return s.submit(a, line), true
}

// knownVerb reports whether head names something the router can run.
func knownVerb(verbs []string, head string) bool {
	switch head {
	case verbHelp, "?", verbClear, verbVersion:
		return true
	}
	for _, v := range verbs {
		if v == head || strings.HasPrefix(v, head+" ") {
			return true
		}
	}
	return false
}

// setModel switches the model the next turn is sent to, refusing anything the
// gateway does not advertise. A model chosen here and refused by the gateway
// would fail one turn later with an error naming neither.
func (s *playScreen) setModel(a *App, id string) {
	id = strings.TrimSpace(id)
	if id == "" {
		s.note(a, "model is "+s.model, false)
		return
	}
	for _, m := range s.models {
		if m == id {
			s.model = id
			s.note(a, "model is "+id, false)
			return
		}
	}
	if len(s.models) == 0 {
		// Nothing to check against is not the same as a bad name; adopt it and
		// let the gateway be the authority, which it is either way.
		s.model = id
		s.note(a, "model is "+id+" (this gateway advertised no model list)", false)
		return
	}
	s.note(a, fmt.Sprintf("no model named %q — try %s", id, strings.Join(s.nearest(id), ", ")), true)
}

// nearest offers the models closest to what was typed: shared prefix first,
// then the head of the list, so a refusal always names something runnable.
func (s *playScreen) nearest(id string) []string {
	var out []string
	for _, m := range s.models {
		if strings.HasPrefix(m, id[:min(len(id), 3)]) {
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		out = s.models
	}
	return out[:min(len(out), modelSuggestions)]
}

// note is how the playground answers a command: as a turn in the conversation,
// because that is where the operator is looking. The transcript is on another
// pane and a red row pushed there would not be seen.
func (s *playScreen) note(_ *App, text string, bad bool) {
	s.appendTurns(chatTurn{Who: whoGateway, Text: text, Bad: bad, Note: true, lines: []string{text}})
}

func (s *playScreen) appendTurns(turns ...chatTurn) {
	for i := range turns {
		s.lastID++
		turns[i].id = s.lastID
	}
	s.turns = append(s.turns, turns...)
	if len(s.turns) <= playTurnMax {
		return
	}
	s.turns = s.turns[len(s.turns)-playTurnMax:]
	// A chat answer belongs with the user prompt immediately before it. If a
	// one-turn note pushed only that prompt out, discard the orphaned answer.
	for i, turn := range s.turns {
		if turn.Note {
			continue
		}
		if turn.Who == whoGateway {
			s.turns = append(s.turns[:i], s.turns[i+1:]...)
		}
		break
	}
}

func (s *playScreen) reset() {
	s.cancel()
	s.gen++
	s.turns, s.usage, s.busy, s.requestID = nil, nil, false, ""
	s.queued = ""
	s.pending.Reset()
}

// submit opens a turn. The operator's line lands immediately and an empty
// gateway turn is opened beside it, so the answer streams into a slot that is
// already on screen rather than appearing all at once at the end.
func (s *playScreen) submit(a *App, prompt string) tea.Cmd {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return nil
	}
	if s.busy {
		s.note(a, "a turn is still streaming — wait for it, or run clear", true)
		return nil
	}
	if s.renderer == nil {
		s.renderer = wrapPlain
	}
	if s.width <= 0 {
		s.width = a.paneWidth()
	}

	s.appendTurns(
		chatTurn{Who: whoYou, Text: prompt, lines: s.wrap(prompt)},
		chatTurn{Who: whoGateway})
	s.usage, s.busy, s.started, s.requestID = nil, true, time.Now(), ""
	s.pending.Reset()

	ctx, cancel := context.WithCancel(context.Background())
	s.openCancel = cancel
	return openChat(ctx, a.Client, s.gen, api.ChatRequest{
		Model:    s.model,
		Messages: s.history(),
	})
}

// history is the conversation as the gateway sees it. Failed turns are left
// out: a turn that ended in a stream error has no answer, and sending its
// partial text back as though the model had said it is a fabrication.
func (s *playScreen) history() []api.ChatMessage {
	out := make([]api.ChatMessage, 0, len(s.turns))
	for _, t := range s.turns {
		if t.Text == "" || t.Bad || t.Note {
			continue
		}
		role := "assistant"
		if t.Who == whoYou {
			role = "user"
		}
		out = append(out, api.ChatMessage{Role: role, Content: t.Text})
	}
	return out
}

// -------------------------------------------------------------------- update

func (s *playScreen) update(a *App, msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case playModelsMsg:
		if m.gen != s.gen {
			return nil
		}
		if m.err == nil {
			s.models = m.models
			if s.model == "" && len(s.models) > 0 {
				s.model = s.models[0]
			}
		}
		// A queued prompt is released either way. A failed listing leaves the
		// gateway as the only authority on which names it accepts, which it is
		// regardless — refusing to ask would be worse than asking and being
		// told why.
		if q := s.queued; q != "" {
			s.queued = ""
			return s.submit(a, q)
		}
		return nil

	case chatOpenMsg:
		if m.gen != s.gen {
			// A stream opened for a visit the operator has left still owns a
			// connection and a goroutine. Cancel it rather than leak it.
			if m.stream != nil {
				m.stream.Cancel()
			}
			return nil
		}
		if m.err != nil {
			return s.fail(a, "gateway: "+m.err.Error())
		}
		if m.stream == nil {
			return s.fail(a, "gateway: stream opened without a response")
		}
		s.stream, s.requestID = m.stream, m.stream.RequestID
		return tea.Batch(readStream(s.gen, m.stream.Events), tickFlush(s.gen))

	case chatDeltaMsg:
		if m.gen != s.gen || !s.busy {
			return nil
		}
		s.pending.WriteString(m.text)
		return s.next()

	case chatUsageMsg:
		if m.gen != s.gen || !s.busy {
			return nil
		}
		s.usage = m.usage
		return s.next()

	case chatEndMsg:
		if m.gen != s.gen || !s.busy {
			return nil
		}
		s.flush(a)
		s.took = time.Since(s.started)
		s.cancel()
		s.busy = false
		switch {
		case m.err != nil:
			s.markFailed(streamFailure(m.err))
			return nil
		case !m.done:
			// The channel closed with neither a terminator nor an error. The
			// answer is truncated and saying so is the whole job.
			s.markFailed("gateway: the stream ended with no terminator — the answer above is truncated")
			return nil
		}
		return s.metaCmd(a)

	case chatMetaMsg:
		if m.gen != s.gen {
			return nil
		}
		for i := range s.turns {
			if s.turns[i].id == m.turn {
				s.turns[i].Meta = metaLine(m.usage, m.row, m.took)
				break
			}
		}
		return nil

	case flushTickMsg:
		if m.gen != s.gen {
			return nil
		}
		s.flush(a)
		if !s.busy {
			return nil
		}
		return tickFlush(s.gen)
	}
	return nil
}

// next keeps the reader pumping. One event per command, re-issued until the
// stream ends — the standard Bubble Tea channel pump.
func (s *playScreen) next() tea.Cmd {
	if s.stream == nil {
		return nil
	}
	return readStream(s.gen, s.stream.Events)
}

// answer is the index of the turn currently being written into.
func (s *playScreen) answer() int {
	for i := len(s.turns) - 1; i >= 0; i-- {
		if s.turns[i].Who == whoGateway && !s.turns[i].Note {
			return i
		}
	}
	return -1
}

// flush moves the batched deltas into the turn and re-renders it ONCE.
func (s *playScreen) flush(_ *App) {
	if s.pending.Len() == 0 {
		return
	}
	text := s.pending.String()
	s.pending.Reset()
	i := s.answer()
	if i < 0 {
		return
	}
	if s.renderer == nil {
		s.renderer = wrapPlain
	}
	s.turns[i].Text += text
	s.turns[i].lines = s.wrap(s.turns[i].Text)
}

// markFailed turns the open answer into a visible failure, keeping whatever
// text arrived before it broke.
func (s *playScreen) markFailed(text string) {
	i := s.answer()
	if i < 0 {
		return
	}
	s.turns[i].Bad = true
	if s.turns[i].Text != "" {
		text = s.turns[i].Text + "\n" + text
	}
	s.turns[i].Text = text
	s.turns[i].lines = s.wrap(text)
}

func (s *playScreen) fail(_ *App, text string) tea.Cmd {
	s.busy = false
	s.cancel()
	s.markFailed(text)
	return nil
}

// metaCmd resolves the route metadata a streamed body cannot carry. It reads
// the turn and the two measurements the answer will be labelled with HERE,
// because everything it reads is about to become the PREVIOUS turn's, and
// hands them to fetchAttribution to carry on the returned command's eventual
// message rather than pinning them in screen state.
//
// That distinction matters because chatEndMsg clears busy before this
// command's result lands, so the operator can submit and finish a SECOND turn
// while the first turn's fetch is still in flight — two fetches really can be
// outstanding at once. A single screen-level slot would let the second turn's
// metaCmd overwrite the turn id, usage and duration the first turn's fetch was
// issued for, so its answer — arriving later — would be applied to the wrong
// turn. Carrying the values on the message and matching on turn id when it
// arrives keeps the two independent regardless of arrival order.
func (s *playScreen) metaCmd(a *App) tea.Cmd {
	var turn int
	if i := s.answer(); i >= 0 {
		turn = s.turns[i].id
	}
	return fetchAttribution(a.Client, s.gen, turn, s.usage, s.took, s.requestID, s.started.Add(-attributionSlack))
}

// streamFailure is the operator-facing wording for each stream error code. It
// matches `ferro chat`'s, so the same failure reads the same in both.
func streamFailure(e *api.Error) string {
	switch e.Code {
	case api.CodeStreamTimeout:
		return "gateway: stream idled out (gateway bound: 2m)"
	case api.CodeStreamIncomplete:
		return "gateway: stream ended mid-answer with no terminator — the answer above is truncated"
	}
	if e.Message == "" {
		return "gateway: stream failed: " + e.Error()
	}
	return "gateway: " + e.Message
}

// metaLine assembles the route readout from the two sources that have it: the
// usage chunk the stream sent, and the request-log row the response's
// X-Request-ID matched. Every segment whose fact is missing is DROPPED — a
// gateway with no log store gets a shorter line, never a guessed provider and
// never a $0.00 that was really "unpriced".
func metaLine(u *api.Usage, row *api.LogEntry, took time.Duration) string {
	var parts []string
	if row != nil && row.Provider != "" {
		parts = append(parts, row.Provider+" → served")
	}
	parts = append(parts, fmt.Sprintf("%.2fs", took.Seconds()))
	if u != nil {
		parts = append(parts, fmt.Sprintf("%d in / %d out", u.PromptTokens, u.CompletionTokens))
	}
	if row != nil && row.CostUSD != nil {
		parts = append(parts, costCell(row.CostUSD))
	}
	return strings.Join(parts, " · ")
}

// --------------------------------------------------------------------- view

func (s *playScreen) view(a *App, w, rows int) string {
	th := a.Theme
	// One sub-line plus at most rows-1 body lines: the tail below is cut to
	// exactly that before it is appended.
	out := append(make([]string, 0, rows), s.subLine(a, w))

	body := make([]string, 0, rows)
	for i, t := range s.turns {
		if i > 0 {
			body = append(body, "")
		}
		label := th.Accent.Bold(th.Mode.Color).Render(t.Who)
		if t.Who != whoYou {
			label = th.Bright.Bold(th.Mode.Color).Render(t.Who)
		}
		body = append(body, label)

		lines := t.lines
		if len(lines) == 0 && t.Text != "" {
			lines = strings.Split(t.Text, "\n")
		}
		for _, line := range lines {
			if t.Bad {
				line = th.Bad.Render(line)
			}
			body = append(body, line)
		}
		if t.Meta != "" {
			body = append(body,
				th.Hairline.Render(a.rule(min(lipgloss.Width(t.Meta), w))),
				th.Dim.Render(t.Meta))
		}
	}
	if len(s.turns) == 0 {
		body = append(body, "", th.Dim.Render("Type a prompt to start. /model switches models, clear resets."))
	}

	// The transcript of a conversation is read from the bottom.
	return fill(append(out, tail(body, max(rows-len(out), 0))...), w, rows)
}

func (s *playScreen) subLine(a *App, w int) string {
	th := a.Theme
	model := s.model
	if model == "" {
		model = "gateway default"
	}
	right := ""
	if s.busy {
		right = th.Accent.Render("streaming…")
		if th.Mode.ASCII {
			right = th.Accent.Render("streaming...")
		}
	}
	return gapPad(w, " "+th.Dim.Render("/model "+model+" · clear resets · esc home"), right)
}

// tail keeps the last n lines. A chat scrolls up, so what falls off the top is
// what the operator has already read.
func tail(lines []string, n int) []string {
	if n <= 0 {
		return nil
	}
	if len(lines) <= n {
		return lines
	}
	return lines[len(lines)-n:]
}

// ---------------------------------------------------------------- rendering

func (s *playScreen) wrap(text string) []string {
	if text == "" {
		return nil
	}
	w := max(s.width, 8)
	out := text
	if s.renderer != nil {
		out = s.renderer(text, w)
	}
	return strings.Split(strings.TrimRight(out, "\n"), "\n")
}

// wrapPlain folds an answer to the pane width. It is ANSI- and grapheme-aware,
// so a wrapped line still measures what it draws.
func wrapPlain(text string, w int) string { return ansi.Wrap(text, max(w, 8), "") }
