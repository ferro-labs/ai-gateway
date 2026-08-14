package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/ferro-labs/ai-gateway/cli/internal/api"
	"github.com/ferro-labs/ai-gateway/cli/internal/tui/theme"
)

func f64(v float64) *float64 { return &v }

// logRow builds one entry with everything measured, so a test that wants an
// absent measurement removes exactly the field it is about.
func logRow(trace string, at time.Time) api.LogEntry {
	return api.LogEntry{
		TraceID: trace, Stage: "response", Model: "claude-sonnet-4-6",
		Provider: "anthropic", APIKeyID: "ak_1",
		PromptTokens: 412, CompletionTokens: 189, TotalTokens: 601,
		CreatedAt: at, DurationMs: f64(934), TTFTMs: f64(210), CostUSD: f64(0.0031),
	}
}

func TestLogsStartParsesFilters(t *testing.T) {
	a := composerApp()
	before := time.Now()
	a.route("logs --since 15m --model claude-* --provider anthropic --stage response --key none --limit 25")

	if a.Screen != ScreenLogs {
		t.Fatalf("a valid filter set enters the screen, got %v", a.Screen)
	}
	q := a.Logs.filters
	if q.Model != "claude-*" {
		t.Fatalf("--model must keep its case and its glob, got %q", q.Model)
	}
	if q.Provider != "anthropic" || q.Stage != "response" {
		t.Fatalf("provider/stage filters dropped: %+v", q)
	}
	if q.APIKeyID != "none" {
		t.Fatalf(`--key none is the gateway's own sentinel and passes through, got %q`, q.APIKeyID)
	}
	if q.Limit != 25 {
		t.Fatalf("--limit dropped, got %d", q.Limit)
	}
	if d := before.Sub(q.Since); d < 14*time.Minute || d > 16*time.Minute {
		t.Fatalf("--since 15m must be 15m into the past, got %v before now", d)
	}
	if !strings.Contains(a.paneView(120, 10), "--model claude-*") {
		t.Fatalf("the active filters must be on screen:\n%s", a.paneView(120, 10))
	}
}

func TestLogsStartRejectsUnknownFlag(t *testing.T) {
	for _, args := range []string{"--frobnicate x", "--since", "--limit banana", "claude-*"} {
		a := composerApp()
		if cmd := a.route("logs " + args); cmd != nil {
			t.Fatalf("%q must not reach the network", args)
		}
		if a.Screen != ScreenHome {
			t.Fatalf("%q must leave the operator on the transcript, got %v", args, a.Screen)
		}
		row := lastRow(a)
		if row.Glyph != "bad" {
			t.Fatalf("%q must be refused in a red row, got %#v", args, row)
		}
		if !strings.Contains(row.Text, "--since") {
			t.Fatalf("a refusal must name the filters that do exist: %q", row.Text)
		}
	}
}

func TestLogsBatchPrependsBounded(t *testing.T) {
	a := composerApp()
	a.route("logs")
	now := time.Now()

	seed := make([]api.LogEntry, 0, 195)
	for i := range 195 {
		seed = append(seed, logRow(fmt.Sprintf("tr_seed%03d", i), now.Add(time.Duration(i)*time.Second)))
	}
	a.update(logsBatchMsg{gen: a.Logs.gen, rows: seed})

	batch := make([]api.LogEntry, 0, 10)
	for i := range 10 {
		batch = append(batch, logRow(fmt.Sprintf("tr_new%03d", i), now.Add(time.Duration(200+i)*time.Second)))
	}
	a.update(logsBatchMsg{gen: a.Logs.gen, rows: batch})

	if got := len(a.Logs.rows); got != logsRing {
		t.Fatalf("the ring is bounded at %d, got %d", logsRing, got)
	}
	// Newest first: the last row of the newest ascending batch leads.
	if a.Logs.rows[0].TraceID != "tr_new009" {
		t.Fatalf("rows must be newest first, got %q", a.Logs.rows[0].TraceID)
	}
	if a.Logs.rows[9].TraceID != "tr_new000" {
		t.Fatalf("a batch must keep its own order when prepended, got %q", a.Logs.rows[9].TraceID)
	}
}

func TestLogsSelectionAndDetail(t *testing.T) {
	a := composerApp()
	a.route("logs")
	now := time.Now()
	rows := []api.LogEntry{logRow("tr_aaaaaaaa", now), logRow("tr_bbbbbbbb", now.Add(time.Second))}
	rows[0].APIKeyID = "ak_revoked"
	rows[1].APIKeyID = "master-key:0f21"
	a.update(logsBatchMsg{gen: a.Logs.gen, rows: rows})
	a.update(logsNamesMsg{gen: a.Logs.gen, names: map[string]keyRef{
		"ak_revoked": {Name: "laptop-old", Revoked: true},
	}})

	if !a.screenKey("down") {
		t.Fatal("the logs pane must take ↓ while the command line is empty")
	}
	if a.Logs.sel != 0 {
		t.Fatalf("↓ with nothing selected takes the newest row, got %d", a.Logs.sel)
	}
	a.screenKey("down")
	if a.Logs.sel != 1 {
		t.Fatalf("↓ must move the selection, got %d", a.Logs.sel)
	}
	a.screenKey("up")
	if a.Logs.sel != 0 {
		t.Fatalf("↑ must move it back, got %d", a.Logs.sel)
	}

	// Row 0 is the newest, which is tr_bbbbbbbb with the master credential.
	a.screenKey("enter")
	view := a.paneView(120, 20)
	if !strings.Contains(view, "tr_bbbbbbbb") {
		t.Fatalf("the strip must carry the full trace id:\n%s", view)
	}
	if !strings.Contains(view, "master key") {
		t.Fatalf("a master-key:* id must resolve to `master key`:\n%s", view)
	}

	a.screenKey("down")
	a.screenKey("enter")
	view = a.paneView(120, 20)
	if !strings.Contains(view, "laptop-old") {
		t.Fatalf("a resolvable api_key_id must render as its name:\n%s", view)
	}
	if !strings.Contains(view, "[revoked]") {
		t.Fatalf("a key revoked since it served the traffic must carry the badge:\n%s", view)
	}

	if !a.screenKey("esc") {
		t.Fatal("esc must close the strip before it means back")
	}
	if a.Logs.detail {
		t.Fatal("esc must close the detail strip")
	}
	if a.screenKey("esc") {
		t.Fatal("with no strip open, esc belongs to the composer")
	}
}

// An unresolvable id is rendered raw. A deleted key keeps its log rows, and
// that is exactly the case this question is most often asked about.
func TestLogsUnresolvableKeyRendersRawID(t *testing.T) {
	a := composerApp()
	a.route("logs")
	row := logRow("tr_cccccccc", time.Now())
	row.APIKeyID = "ak_deleted"
	a.update(logsBatchMsg{gen: a.Logs.gen, rows: []api.LogEntry{row}})
	a.Logs.sel, a.Logs.detail = 0, true
	if v := a.paneView(120, 20); !strings.Contains(v, "ak_deleted") {
		t.Fatalf("an id no key store can name must render as itself:\n%s", v)
	}
}

func TestLogsPollFailureKeepsRowsShowsStale(t *testing.T) {
	a := composerApp()
	a.route("logs")
	a.update(logsBatchMsg{gen: a.Logs.gen, rows: []api.LogEntry{logRow("tr_keepme0", time.Now())}})

	base := a.Logs.backoff
	a.update(logsBatchMsg{gen: a.Logs.gen, err: errors.New("connection refused")})
	if len(a.Logs.rows) != 1 {
		t.Fatal("a failed poll must never blank the table")
	}
	if a.Logs.backoff != 2*base {
		t.Fatalf("a failure must double the cadence, got %v", a.Logs.backoff)
	}
	if a.Logs.staleAt.IsZero() {
		t.Fatal("a failed poll must record when the data went stale")
	}
	view := a.paneView(120, 12)
	if !strings.Contains(view, "tr_keepme0") {
		t.Fatalf("the rows already shown must survive:\n%s", view)
	}
	if !strings.Contains(view, "stale") || !strings.Contains(view, "retrying") {
		t.Fatalf("stale data must say so and say it is still trying:\n%s", view)
	}

	for range 10 {
		a.update(logsBatchMsg{gen: a.Logs.gen, err: errors.New("still down")})
	}
	if a.Logs.backoff != maxPoll {
		t.Fatalf("backoff must cap at %v, got %v", maxPoll, a.Logs.backoff)
	}

	a.update(logsBatchMsg{gen: a.Logs.gen, rows: []api.LogEntry{logRow("tr_backnow", time.Now())}})
	if a.Logs.backoff != logsPoll || !a.Logs.staleAt.IsZero() || a.Logs.lastErr != nil {
		t.Fatalf("one good poll clears the staleness: backoff=%v stale=%v err=%v",
			a.Logs.backoff, a.Logs.staleAt, a.Logs.lastErr)
	}
}

func TestLogsNullsRenderDash(t *testing.T) {
	a := composerApp()
	a.route("logs")
	row := logRow("tr_nulls000", time.Now())
	row.DurationMs, row.TTFTMs, row.CostUSD = nil, nil, nil
	a.update(logsBatchMsg{gen: a.Logs.gen, rows: []api.LogEntry{row}})
	a.Logs.sel, a.Logs.detail = 0, true

	view := a.paneView(126, 20)
	line := findRow(view, "tr_nulls")
	if line == "" {
		t.Fatalf("the row must render:\n%s", view)
	}
	for _, bad := range []string{"0.0000", "$0.00", " 0 ", "0ms"} {
		if strings.Contains(line, bad) {
			t.Fatalf("an unmeasured value must never render as %q: %q", bad, line)
		}
	}
	if !strings.Contains(line, "-") {
		t.Fatalf("an unmeasured value renders as a dash: %q", line)
	}
	for _, label := range []string{"duration", "ttft", "cost"} {
		if r := findRow(view, label); !strings.Contains(r, "-") {
			t.Fatalf("strip row %q must render a dash for an absent measurement: %q", label, r)
		}
	}
}

func TestLogsTickIgnoredAfterLeave(t *testing.T) {
	a := fixtureApp(t)
	a.route("logs")
	gen := a.Logs.gen
	if !a.Logs.tailing {
		t.Fatal("entering the logs screen starts the tail")
	}

	// esc is how an operator leaves, and it goes through the composer.
	a.update(runCmdMsg{raw: "help"})
	if a.Screen != ScreenHome {
		t.Fatalf("help must leave the logs screen, got %v", a.Screen)
	}
	if a.Logs.tailing {
		t.Fatal("leaving the screen must stop the tail")
	}
	if cmd := a.update(tickLogsMsg{gen: gen}); cmd != nil {
		t.Fatal("a tick from a screen the operator has left must schedule nothing")
	}
	if cmd := a.update(tickLogsMsg{gen: a.Logs.gen}); cmd != nil {
		t.Fatal("a tick while the pane is off screen must schedule nothing either")
	}
	// A batch that lands late must not repopulate a pane nobody is watching.
	a.update(logsBatchMsg{gen: gen, rows: []api.LogEntry{logRow("tr_late0000", time.Now())}})
	if len(a.Logs.rows) != 0 {
		t.Fatalf("a late batch must be dropped, got %d rows", len(a.Logs.rows))
	}
}

// A gateway with no request-log store answers 501 to every poll. That is an
// absent feature, not an outage: the pane names it, and the tail stops rather
// than backing off against something that will never succeed.
func TestLogsNoStoreStopsTailingAndSaysWhy(t *testing.T) {
	a := fixtureApp(t)
	a.route("logs")
	a.update(logsBatchMsg{gen: a.Logs.gen, err: &api.Error{
		Status: 501, Code: "not_supported", Message: "no request log store configured",
	}})

	if a.Logs.tailing {
		t.Fatal("a 501 will not become a 200 — the tail must stop")
	}
	if !a.Logs.staleAt.IsZero() || a.Logs.backoff != logsPoll {
		t.Fatalf("an absent feature is not stale data: stale=%v backoff=%v", a.Logs.staleAt, a.Logs.backoff)
	}
	view := a.paneView(110, 10)
	if !strings.Contains(view, "no request-log store configured") {
		t.Fatalf("the pane must name the missing store:\n%s", view)
	}
	for _, bad := range []string{"stale", "retrying", "501"} {
		if strings.Contains(view, bad) {
			t.Fatalf("an absent store must not read as a failure (%q):\n%s", bad, view)
		}
	}
}

func TestLogsNoConnectionSaysSoWithoutTailing(t *testing.T) {
	a := composerApp() // no client
	a.route("logs")
	if a.Logs.tailing {
		t.Fatal("there is nothing to tail without a connection")
	}
	if v := a.paneView(110, 10); !strings.Contains(v, errNoConnection.Error()) {
		t.Fatalf("the pane must say why it is empty:\n%s", v)
	}
}

func TestLogsSelectionClearsWhenItsRowIsEvicted(t *testing.T) {
	var s logsScreen
	base := time.Now()
	for i := range logsRing {
		s.rows = append(s.rows, logRow(fmt.Sprintf("tr_%08d", i), base.Add(-time.Duration(i)*time.Second)))
	}
	s.sel, s.detail = len(s.rows)-1, true
	s.prepend([]api.LogEntry{logRow("tr_new", base.Add(time.Second))})
	if s.sel != -1 || s.detail {
		t.Fatalf("evicted selection must close its detail strip: sel=%d detail=%v", s.sel, s.detail)
	}
}

func TestLogsScreenKeepsThePaneContract(t *testing.T) {
	a := composerApp()
	a.route("logs --since 15m --model claude-sonnet-4-6")
	now := time.Now()
	rows := make([]api.LogEntry, 0, 12)
	for i := range 12 {
		r := logRow(fmt.Sprintf("tr_%08d", i), now.Add(time.Duration(i)*time.Second))
		if i%3 == 0 {
			r.ErrorMessage = "upstream error from provider anthropic"
			r.Stage = "on_error"
		}
		rows = append(rows, r)
	}
	a.update(logsBatchMsg{gen: a.Logs.gen, rows: rows})
	a.Logs.sel, a.Logs.detail = 4, true

	for _, mode := range []theme.Mode{{}, {Color: true}, {ASCII: true}, {Color: true, ASCII: true}} {
		a.Theme = theme.New(mode)
		for _, rowsN := range []int{1, 4, 9, 20} {
			for _, w := range []int{30, 60, 92, 126} {
				body := a.paneView(w, rowsN)
				if got := len(strings.Split(body, "\n")); got != rowsN {
					t.Fatalf("mode=%+v w=%d rows=%d returned %d lines", mode, w, rowsN, got)
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

// api.Follower is one-per-tail-loop state — its cursor, dedupe map and row
// slice are unsynchronised — so the tail must never have two polls in flight.
// The next tick is armed by the arm that handles a finished poll, and nowhere
// else: a gateway whose /admin/logs takes longer than the interval is all it
// would take to overlap two otherwise.
func TestLogsPollsAreSerialised(t *testing.T) {
	a := fixtureApp(t)
	a.route("logs")

	// A tick asks for ONE poll and schedules nothing. If it also armed the next
	// tick, this command would answer with a batch instead of a poll result.
	cmd := a.update(tickLogsMsg{gen: a.Logs.gen})
	if cmd == nil {
		t.Fatal("a tick on a live tail must poll")
	}
	if msg := cmd(); !isLogsBatch(msg) {
		t.Fatalf("a tick must produce exactly one poll, got %T", msg)
	}

	// The finished poll is what arms the next tick — on both of its outcomes.
	if cmd = a.update(logsBatchMsg{gen: a.Logs.gen,
		rows: []api.LogEntry{logRow("tr_serial01", time.Now())}}); cmd == nil {
		t.Fatal("a finished poll must arm the next tick")
	}
	// Read the armed command on the failure path, where the cadence is ours to
	// shrink: a good poll resets it to logsPoll and running that would sleep.
	a.Logs.backoff = time.Millisecond
	cmd = a.update(logsBatchMsg{gen: a.Logs.gen, err: errors.New("connection refused")})
	if cmd == nil {
		t.Fatal("a failed poll must arm the retry")
	}
	if msg := cmd(); !isTickLogs(msg) {
		t.Fatalf("a finished poll arms a tick, not another poll, got %T", msg)
	}
}

func isLogsBatch(msg any) bool { _, ok := msg.(logsBatchMsg); return ok }
func isTickLogs(msg any) bool  { _, ok := msg.(tickLogsMsg); return ok }

// A tail that has stopped arms nothing, or the loop outlives the screen.
func TestLogsStoppedTailArmsNothing(t *testing.T) {
	a := fixtureApp(t)
	a.route("logs")
	if cmd := a.update(logsBatchMsg{gen: a.Logs.gen, err: &api.Error{
		Status: 501, Code: "not_supported", Message: "no request log store configured",
	}}); cmd != nil {
		t.Fatal("a 501 stops the tail — it must not arm another poll")
	}
}

// ErrFollowTruncated arrives WITH rows: the poll worked and the cursor moved,
// and what was lost is the oldest end of that one window. It is a readout
// beside real rows, not a failure — backing off or dropping the rows would
// punish a gateway whose only problem is that it is busy.
func TestLogsTruncatedWindowKeepsRowsAndSaysSo(t *testing.T) {
	a := fixtureApp(t)
	a.route("logs")
	a.update(logsBatchMsg{
		gen:  a.Logs.gen,
		rows: []api.LogEntry{logRow("tr_trunc001", time.Now())},
		err:  api.ErrFollowTruncated,
	})

	if len(a.Logs.rows) != 1 {
		t.Fatalf("the rows a truncated poll did return are real, got %d", len(a.Logs.rows))
	}
	if a.Logs.lastErr != nil || !a.Logs.staleAt.IsZero() || a.Logs.backoff != logsPoll {
		t.Fatalf("a truncated window is not a failed poll: err=%v stale=%v backoff=%v",
			a.Logs.lastErr, a.Logs.staleAt, a.Logs.backoff)
	}
	if !a.Logs.tailing {
		t.Fatal("a truncated window must not stop the tail")
	}
	if view := a.paneView(110, 10); !strings.Contains(view, "older rows") {
		t.Fatalf("the pane must say which end of the window was cut:\n%s", view)
	}

	// The next clean poll takes the notice back down.
	a.update(logsBatchMsg{gen: a.Logs.gen, rows: []api.LogEntry{logRow("tr_trunc002", time.Now())}})
	if a.Logs.truncated {
		t.Fatal("a clean poll clears the truncation notice")
	}
}
