package tui

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/ferro-labs/ai-gateway/cli/internal/api"
	"github.com/ferro-labs/ai-gateway/cli/internal/tui/theme"
)

// stubStream is a ChatStream whose events the test writes itself. Cancel is
// counted rather than asserted once, because leaving a screen and finishing a
// turn can both reach it and both must be safe.
func stubStream(events ...api.StreamEvent) (*api.ChatStream, *atomic.Int32) {
	ch := make(chan api.StreamEvent, len(events)+1)
	for _, e := range events {
		ch <- e
	}
	close(ch)
	var cancels atomic.Int32
	return &api.ChatStream{
		RequestID: "tr_stub0001",
		Events:    ch,
		Cancel:    func() { cancels.Add(1) },
	}, &cancels
}

func delta(text string) api.StreamEvent {
	return api.StreamEvent{Chunk: &api.ChatChunk{
		Choices: []api.StreamChoice{{Delta: api.MessageDelta{Content: text}}},
	}}
}

func usageEvent(in, out int) api.StreamEvent {
	return api.StreamEvent{Chunk: &api.ChatChunk{
		Usage: &api.Usage{PromptTokens: in, CompletionTokens: out, TotalTokens: in + out},
	}}
}

// pump drains a stream into the app exactly as the reader command does.
func pump(a *App, st *api.ChatStream) {
	for range 64 {
		msg := readStream(a.Play.gen, st.Events)()
		a.update(msg)
		if _, done := msg.(chatEndMsg); done {
			return
		}
	}
}

func lastTurn(a *App) chatTurn {
	if n := len(a.Play.turns); n > 0 {
		return a.Play.turns[n-1]
	}
	return chatTurn{}
}

func TestSubmitAppendsTurnsAndStreams(t *testing.T) {
	a := composerApp()
	a.route("playground")
	a.Play.submit(a, "which target serves sonnet?")

	if len(a.Play.turns) != 2 {
		t.Fatalf("a submission opens the operator's turn and the answer's, got %d", len(a.Play.turns))
	}
	if a.Play.turns[0].Who != "you" || a.Play.turns[0].Text != "which target serves sonnet?" {
		t.Fatalf("the prompt must be echoed verbatim, got %#v", a.Play.turns[0])
	}
	if !a.Play.busy {
		t.Fatal("a submitted turn is busy until the stream ends")
	}

	st, cancels := stubStream(delta("Ferro "), delta("routed "), delta("this."),
		usageEvent(318, 142), api.StreamEvent{Done: true})
	a.update(chatOpenMsg{gen: a.Play.gen, stream: st})
	pump(a, st)

	if a.Play.busy {
		t.Fatal("[DONE] ends the turn")
	}
	if got := lastTurn(a).Text; got != "Ferro routed this." {
		t.Fatalf("every delta must land in the turn, got %q", got)
	}
	if a.Play.usage == nil || a.Play.usage.PromptTokens != 318 {
		t.Fatalf("the usage chunk must be kept, got %+v", a.Play.usage)
	}
	if cancels.Load() == 0 {
		t.Fatal("Cancel releases the reader and the connection even after a clean Done")
	}
	if v := a.paneView(120, 20); !strings.Contains(v, "Ferro routed this.") {
		t.Fatalf("the answer must be on screen:\n%s", v)
	}
}

func TestLeavingPlaygroundCancelsStreamBeforeHeaders(t *testing.T) {
	requestStarted := make(chan struct{})
	releaseServer := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		close(requestStarted)
		<-releaseServer
	}))
	defer func() {
		close(releaseServer)
		srv.Close()
	}()
	c, err := api.New(srv.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	a := composerApp()
	a.Client = c
	a.route("playground")
	cmd := a.Play.submit(a, "hello")
	done := make(chan struct{})
	go func() {
		_ = cmd()
		close(done)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	select {
	case <-requestStarted:
	case <-ctx.Done():
		t.Fatal("the stream-opening request never reached the server")
	}
	a.Play.leave()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("the stream-opening command did not return after cancellation")
	}
}

// The whole point of the flush tick: one markdown render per batch, never one
// per token.
func TestFlushCadenceBatchesDeltas(t *testing.T) {
	a := composerApp()
	a.route("playground")

	var renders atomic.Int32
	a.Play.renderer = func(text string, _ int) string {
		renders.Add(1)
		return text
	}
	a.Play.submit(a, "hello")
	renders.Store(0)

	for range 10 {
		a.update(chatDeltaMsg{gen: a.Play.gen, text: "word "})
	}
	if n := renders.Load(); n != 0 {
		t.Fatalf("a delta must not re-render; %d renders before the flush", n)
	}
	if lastTurn(a).Text != "" {
		t.Fatal("deltas are batched, not written straight into the turn")
	}

	a.update(flushTickMsg{gen: a.Play.gen})
	if n := renders.Load(); n != 1 {
		t.Fatalf("one flush is one render, got %d", n)
	}
	if got := lastTurn(a).Text; got != strings.Repeat("word ", 10) {
		t.Fatalf("the flush must move every batched delta, got %q", got)
	}
}

func TestMetaLineFromUsageAndAttribution(t *testing.T) {
	u := &api.Usage{PromptTokens: 318, CompletionTokens: 142, TotalTokens: 460}
	row := &api.LogEntry{Provider: "anthropic", CostUSD: f64(0.0031)}

	got := metaLine(u, row, 930*time.Millisecond)
	for _, want := range []string{"anthropic", "served", "0.93s", "318 in / 142 out", "$0.0031"} {
		if !strings.Contains(got, want) {
			t.Fatalf("meta line missing %q: %q", want, got)
		}
	}
}

// A gateway with no log store answers 501, which TraceAttribution reports as
// (nil, nil). The line must lose the segments it cannot support and keep the
// ones it measured itself — never invent a provider or a cost.
func TestMetaWithoutLogStore(t *testing.T) {
	u := &api.Usage{PromptTokens: 318, CompletionTokens: 142}
	got := metaLine(u, nil, 930*time.Millisecond)
	if !strings.Contains(got, "318 in / 142 out") || !strings.Contains(got, "0.93s") {
		t.Fatalf("what was measured must survive: %q", got)
	}
	for _, bad := range []string{"served", "$", "→"} {
		if strings.Contains(got, bad) {
			t.Fatalf("an unattributed turn must not carry %q: %q", bad, got)
		}
	}

	// And with no usage chunk either, the elapsed time is all there is.
	if got := metaLine(nil, nil, time.Second); got != "1.00s" {
		t.Fatalf("a bare turn reports only what it timed, got %q", got)
	}
}

func TestSlashModelValidates(t *testing.T) {
	a := composerApp()
	a.route("playground")
	a.update(playModelsMsg{gen: a.Play.gen, models: []string{
		"claude-sonnet-4-6", "claude-opus-4-6", "claude-haiku-4-5", "gpt-4o-mini", "gpt-4o",
	}})
	if a.Play.model != "claude-sonnet-4-6" {
		t.Fatalf("the first advertised model is the default, got %q", a.Play.model)
	}

	a.update(runCmdMsg{raw: "model gpt-4o"})
	if a.Play.model != "gpt-4o" {
		t.Fatalf("/model must switch to an advertised model, got %q", a.Play.model)
	}
	if a.Screen != ScreenPlayground {
		t.Fatal("/model is a playground action, not a command that leaves it")
	}

	a.update(runCmdMsg{raw: "model claude-nope"})
	if a.Play.model != "gpt-4o" {
		t.Fatalf("an unknown model must not be adopted, got %q", a.Play.model)
	}
	turn := lastTurn(a)
	if !turn.Bad {
		t.Fatalf("an unknown model must be refused visibly, got %#v", turn)
	}
	if !strings.Contains(turn.Text, "claude-sonnet-4-6") {
		t.Fatalf("a refusal must suggest the models that do exist: %q", turn.Text)
	}
	if strings.Contains(transcriptText(a), "ferro model") {
		t.Fatalf("a playground action is not a shell verb and must not be echoed:\n%s", transcriptText(a))
	}
}

func TestPlaygroundNotesNeverEnterModelHistoryOrReceiveDeltas(t *testing.T) {
	a := composerApp()
	a.route("playground")
	a.Play.renderer = wrapPlain
	a.Play.width = 80
	a.Play.appendTurns(
		chatTurn{Who: "you", Text: "hello"},
		chatTurn{Who: "gateway", Text: "answer"},
	)
	a.Play.note(a, "model is gpt-4o", false)

	history := a.Play.history()
	if len(history) != 2 || history[0].Content != "hello" || history[1].Content != "answer" {
		t.Fatalf("console notes must not be sent as assistant messages: %+v", history)
	}
	a.Play.pending.WriteString(" delta")
	a.Play.flush(a)
	if lastTurn(a).Text != "model is gpt-4o" || a.Play.turns[1].Text != "answer delta" {
		t.Fatalf("stream deltas must target the chat answer, not the note: %+v", a.Play.turns)
	}
}

func TestPlaygroundTranscriptIsBounded(t *testing.T) {
	s := playScreen{}
	s.appendTurns(
		chatTurn{Who: "you", Text: "old prompt"}, chatTurn{Who: "gateway", Text: "old answer"},
		chatTurn{Who: "you", Text: "kept prompt"}, chatTurn{Who: "gateway", Text: "kept answer"},
	)
	for i := 0; i < playTurnMax-3; i++ {
		s.appendTurns(chatTurn{Who: "gateway", Text: "note", Note: true})
	}
	if len(s.turns) > playTurnMax || len(s.turns) < 2 || s.turns[0].Who != "you" || s.turns[1].Who != "gateway" {
		t.Fatalf("retention must remove orphaned answers and preserve pairing: %+v", s.turns[:min(3, len(s.turns))])
	}
}

func TestNilOpenedStreamFailsVisibly(t *testing.T) {
	a := composerApp()
	a.route("playground")
	a.Play.submit(a, "hello")
	a.update(chatOpenMsg{gen: a.Play.gen})
	if a.Play.busy || !lastTurn(a).Bad || !strings.Contains(lastTurn(a).Text, "without a response") {
		t.Fatalf("nil stream must become a visible failed turn: %#v", lastTurn(a))
	}
}

func TestLeaveCancelsStream(t *testing.T) {
	a := composerApp()
	a.route("playground")
	a.Play.submit(a, "hello")
	st, cancels := stubStream(delta("one "), delta("two "))
	a.update(chatOpenMsg{gen: a.Play.gen, stream: st})
	gen := a.Play.gen

	a.update(chatDeltaMsg{gen: gen, text: "one "})
	a.update(runCmdMsg{raw: "help"}) // leaves the screen

	if cancels.Load() == 0 {
		t.Fatal("leaving the playground must cancel the stream in flight")
	}
	if a.Play.busy {
		t.Fatal("a cancelled turn is not busy")
	}
	before := lastTurn(a).Text
	a.update(chatDeltaMsg{gen: gen, text: "two "})
	a.update(chatUsageMsg{gen: gen, usage: &api.Usage{PromptTokens: 9}})
	a.update(chatEndMsg{gen: gen, done: true})
	if lastTurn(a).Text != before {
		t.Fatalf("a delta from a screen the operator has left must be dropped, got %q", lastTurn(a).Text)
	}
	if a.Play.usage != nil {
		t.Fatal("a late usage chunk must be dropped too")
	}
}

// Every stream failure ends as a turn an operator can see. stream_incomplete
// is the one most easily lost: it is ferro's own synthesis for a connection
// that ended with neither a terminator nor an explanation.
func TestStreamErrorRendersRedTurn(t *testing.T) {
	for _, tc := range []struct {
		code, message, want string
	}{
		{api.CodeStreamError, "upstream error from provider anthropic", "upstream error from provider anthropic"},
		{api.CodeStreamTimeout, "idle bound elapsed", "stream idled out"},
		{api.CodeStreamIncomplete, "", "terminator"},
	} {
		a := composerApp()
		a.route("playground")
		a.Play.submit(a, "hello")
		st, cancels := stubStream(delta("partial "),
			api.StreamEvent{Err: &api.Error{Status: 200, Code: tc.code, Message: tc.message}})
		a.update(chatOpenMsg{gen: a.Play.gen, stream: st})
		pump(a, st)

		turn := lastTurn(a)
		if !turn.Bad {
			t.Fatalf("%s must render as a failed turn, got %#v", tc.code, turn)
		}
		if !strings.Contains(turn.Text, tc.want) {
			t.Fatalf("%s must say %q, got %q", tc.code, tc.want, turn.Text)
		}
		if a.Play.busy {
			t.Fatalf("%s ends the turn", tc.code)
		}
		if cancels.Load() == 0 {
			t.Fatalf("%s must still release the stream", tc.code)
		}
		if v := a.paneView(110, 20); !strings.Contains(v, tc.want) {
			t.Fatalf("%s must be visible on screen:\n%s", tc.code, v)
		}
	}
}

// A channel that closes with neither Done nor an error frame is the same
// truncation, arriving a different way.
func TestSilentStreamCloseEndsTheTurn(t *testing.T) {
	a := composerApp()
	a.route("playground")
	a.Play.submit(a, "hello")
	st, _ := stubStream(delta("partial "))
	a.update(chatOpenMsg{gen: a.Play.gen, stream: st})
	pump(a, st)

	if a.Play.busy {
		t.Fatal("a closed channel ends the turn")
	}
	if turn := lastTurn(a); !turn.Bad || !strings.Contains(turn.Text, "partial") {
		t.Fatalf("a truncated answer keeps what arrived and says it was cut: %#v", turn)
	}
}

// The seam the plan got wrong: the composer strips the leading slash before
// route() sees the line, so "is this a command" cannot key on it.
func TestPlaygroundRoutesVerbsAndPromptsApart(t *testing.T) {
	a := composerApp()
	a.route("playground")

	a.update(runCmdMsg{raw: "status"})
	if a.Screen != ScreenHome {
		t.Fatal("a line naming a verb is a command, even on the playground")
	}
	if !strings.Contains(transcriptText(a), "ferro status") {
		t.Fatalf("a verb must still be echoed:\n%s", transcriptText(a))
	}

	a.route("playground")
	a.update(runCmdMsg{raw: "which target is serving Sonnet right now?"})
	if a.Screen != ScreenPlayground {
		t.Fatal("a prompt keeps the operator in the playground")
	}
	if got := a.Play.turns[0].Text; got != "which target is serving Sonnet right now?" {
		t.Fatalf("a prompt must reach the model with its case intact, got %q", got)
	}
	if strings.Contains(transcriptText(a), "which target") {
		t.Fatalf("a prompt is not a command and must not be echoed as one:\n%s", transcriptText(a))
	}
}

func TestPlaygroundClearResetsTurnsNotTranscript(t *testing.T) {
	a := composerApp()
	a.route("status")
	a.route("playground")
	a.Play.submit(a, "hello")
	st, cancels := stubStream(delta("hi"))
	a.update(chatOpenMsg{gen: a.Play.gen, stream: st})

	a.update(runCmdMsg{raw: "clear"})
	if len(a.Play.turns) != 0 {
		t.Fatalf("/clear empties the conversation, got %d turns", len(a.Play.turns))
	}
	if cancels.Load() == 0 {
		t.Fatal("/clear must cancel a stream it is discarding the answer of")
	}
	if a.Screen != ScreenPlayground {
		t.Fatal("/clear stays in the playground")
	}
	if a.Transcript.Len() == 0 {
		t.Fatal("/clear on the playground is not the transcript's clear")
	}
}

// End to end against the fake: real SSE framing, real decode, real attribution.
func TestPlaygroundStreamsAgainstTheFixture(t *testing.T) {
	a := fixtureApp(t)
	a.route("playground")
	a.Play.model = "claude-sonnet-4-6"

	msg := a.Play.submit(a, "hello")()
	open, ok := msg.(chatOpenMsg)
	if !ok || open.err != nil {
		t.Fatalf("opening a stream against the fake must succeed, got %#v", msg)
	}
	a.update(open)
	pump(a, open.stream)
	a.update(flushTickMsg{gen: a.Play.gen})

	if got := lastTurn(a).Text; !strings.Contains(got, "Ferro routed this through the fake gateway.") {
		t.Fatalf("the fake's whole answer must arrive, got %q", got)
	}
	if a.Play.usage == nil || a.Play.usage.PromptTokens != 24 {
		t.Fatalf("the usage chunk must be decoded, got %+v", a.Play.usage)
	}

	// The end arm returns the attribution fetch; run it and apply the meta. This
	// is the whole loop the plan calls out: a streamed body names no provider,
	// so the answer's X-Request-ID is matched against the request log.
	a.update(a.Play.metaCmd(a)())
	meta := lastTurn(a).Meta
	for _, want := range []string{"anthropic → served", "24 in / 7 out", "$0.0031"} {
		if !strings.Contains(meta, want) {
			t.Fatalf("the meta line must carry %q, got %q", want, meta)
		}
	}
	t.Logf("meta against the fixture: %q", meta)
}

func TestPlaygroundKeepsThePaneContract(t *testing.T) {
	a := composerApp()
	a.route("playground")
	a.Play.submit(a, "a fairly long prompt that will need wrapping at every width we test")
	a.update(chatDeltaMsg{gen: a.Play.gen, text: "# Heading\n\nSome **markdown** with a list:\n\n- one\n- two\n\n```go\nfmt.Println(\"hi\")\n```\n"})
	a.update(flushTickMsg{gen: a.Play.gen})
	a.update(chatEndMsg{gen: a.Play.gen, done: true})
	a.update(chatMetaMsg{gen: a.Play.gen, turn: lastTurn(a).id, row: &api.LogEntry{Provider: "anthropic", CostUSD: f64(0.0031)}})

	for _, mode := range []theme.Mode{{}, {Color: true}, {ASCII: true}, {Color: true, ASCII: true}} {
		a.Theme = theme.New(mode)
		// The renderer must be REAL: resize returns early without one, so a nil
		// renderer would leave every turn wrapped for whatever width came
		// before and this loop would measure a cache instead of the wrapping
		// it exists to check.
		a.Play.renderer = wrapPlain
		for _, rows := range []int{1, 4, 9, 20} {
			for _, w := range []int{30, 60, 92, 126} {
				a.Play.width = 0 // force the re-wrap regardless of the previous width
				a.Play.resize(w)
				body := a.paneView(w, rows)
				if got := len(strings.Split(body, "\n")); got != rows {
					t.Fatalf("mode=%+v w=%d rows=%d returned %d lines", mode, w, rows, got)
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

// Attribution is resolved from the request log AFTER the stream ends, with one
// retry 500ms later, so the operator can easily submit the next prompt inside
// that window. Everything the meta line is built from — which turn is last, the
// usage chunk, the elapsed time — is about to describe a different turn by the
// time the answer lands, so all of it is pinned when the fetch is ISSUED.
func TestAttributionStaysOnTheTurnThatEarnedIt(t *testing.T) {
	a := composerApp()
	a.route("playground")

	a.Play.submit(a, "which target serves sonnet?")
	a.update(chatUsageMsg{gen: a.Play.gen, usage: &api.Usage{PromptTokens: 24, CompletionTokens: 7}})
	a.update(chatEndMsg{gen: a.Play.gen, done: true}) // issues the attribution fetch
	turn := lastTurn(a).id

	// The operator does not wait for it, and the second prompt resets both the
	// usage and the answer position the old code resolved against.
	a.Play.submit(a, "and haiku?")
	a.update(chatMetaMsg{gen: a.Play.gen, turn: turn, usage: &api.Usage{PromptTokens: 24, CompletionTokens: 7},
		row: &api.LogEntry{Provider: "anthropic", CostUSD: f64(0.0031)}})

	if n := len(a.Play.turns); n != 4 {
		t.Fatalf("two prompts and two answers, got %d turns", n)
	}
	first := a.Play.turns[1].Meta
	for _, want := range []string{"anthropic", "24 in / 7 out", "$0.0031"} {
		if !strings.Contains(first, want) {
			t.Fatalf("the answered turn must keep its own attribution %q, got %q", want, first)
		}
	}
	if got := lastTurn(a).Meta; got != "" {
		t.Fatalf("the turn still streaming has earned no attribution, got %q", got)
	}
}

// Two turns' attribution fetches CAN be outstanding at the same time: chatEndMsg
// clears busy before its fetch lands, so nothing stops the operator from
// submitting and finishing a second turn before the first turn's fetch has
// answered. Both fetches are issued here before either's chatMetaMsg is
// delivered, delivered out of submission order, and each must still land on
// the turn that earned it.
func TestOverlappingAttributionsResolveToTheirOwnTurn(t *testing.T) {
	a := composerApp()
	a.route("playground")

	a.Play.submit(a, "which target serves sonnet?")
	a.update(chatUsageMsg{gen: a.Play.gen, usage: &api.Usage{PromptTokens: 24, CompletionTokens: 7}})
	fetch1 := a.update(chatEndMsg{gen: a.Play.gen, done: true}) // turn 1's fetch is now in flight
	turn1 := lastTurn(a).id

	a.Play.submit(a, "and haiku?")
	a.update(chatUsageMsg{gen: a.Play.gen, usage: &api.Usage{PromptTokens: 11, CompletionTokens: 3}})
	fetch2 := a.update(chatEndMsg{gen: a.Play.gen, done: true}) // turn 2 ends before turn 1's fetch has landed
	turn2 := lastTurn(a).id

	if fetch1 == nil || fetch2 == nil || turn1 == turn2 {
		t.Fatalf("both turns must issue their own attribution fetch, got fetch1=%v fetch2=%v turn1=%d turn2=%d",
			fetch1 != nil, fetch2 != nil, turn1, turn2)
	}

	// Turn 2's row lands first — arrival order is the opposite of submission
	// order, and must not matter.
	a.update(chatMetaMsg{gen: a.Play.gen, turn: turn2, usage: &api.Usage{PromptTokens: 11, CompletionTokens: 3},
		took: 2 * time.Second, row: &api.LogEntry{Provider: "openai", CostUSD: f64(0.0009)}})
	a.update(chatMetaMsg{gen: a.Play.gen, turn: turn1, usage: &api.Usage{PromptTokens: 24, CompletionTokens: 7},
		took: time.Second, row: &api.LogEntry{Provider: "anthropic", CostUSD: f64(0.0031)}})

	if n := len(a.Play.turns); n != 4 {
		t.Fatalf("two prompts and two answers, got %d turns", n)
	}
	first, second := a.Play.turns[1].Meta, a.Play.turns[3].Meta
	for _, want := range []string{"anthropic", "24 in / 7 out", "$0.0031"} {
		if !strings.Contains(first, want) {
			t.Fatalf("turn 1 must keep its own provider, usage and duration, got %q", first)
		}
	}
	for _, want := range []string{"openai", "11 in / 3 out", "$0.0009"} {
		if !strings.Contains(second, want) {
			t.Fatalf("turn 2 must keep its own provider, usage and duration, got %q", second)
		}
	}
}

// `chat <prompt>` opens the pane and asks in one line, which means the ask
// races the model listing that same command starts. submit copies s.model into
// the request there and then, so a prompt sent before discovery lands names no
// model at all — and a gateway that would have served it refuses instead.
func TestInlineChatWaitsForModelDiscovery(t *testing.T) {
	a := fixtureApp(t)
	a.route("chat which target serves sonnet?")

	if len(a.Play.turns) != 0 {
		t.Fatalf("nothing may be sent before a model is known, got %d turns", len(a.Play.turns))
	}
	if a.Play.queued == "" {
		t.Fatal("the prompt must be held, not dropped")
	}

	a.Play.update(a, playModelsMsg{gen: a.Play.gen, models: []string{"claude-sonnet-4-6", "gpt-4o"}})

	if a.Play.model == "" {
		t.Fatal("discovery must name the default before the held prompt is sent")
	}
	if len(a.Play.turns) != 2 || a.Play.turns[0].Text != "which target serves sonnet?" {
		t.Fatalf("the held prompt must be sent once discovery lands, got %+v", a.Play.turns)
	}
	if a.Play.queued != "" {
		t.Fatal("a released prompt must not be sent twice")
	}
}

// A gateway that will not list its models is still the authority on which
// names it accepts. Holding the prompt for a listing that never arrives would
// swallow it silently, which is worse than asking and being told why.
func TestInlineChatReleasesThePromptWhenDiscoveryFails(t *testing.T) {
	a := fixtureApp(t)
	a.route("chat which target serves sonnet?")
	a.Play.update(a, playModelsMsg{gen: a.Play.gen, err: errors.New("connection refused")})

	if len(a.Play.turns) != 2 {
		t.Fatalf("a failed listing must not swallow the prompt, got %+v", a.Play.turns)
	}
}
