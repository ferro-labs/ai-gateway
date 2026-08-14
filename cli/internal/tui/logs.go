package tui

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/ferro-labs/ai-gateway/cli/internal/api"
	"github.com/ferro-labs/ai-gateway/cli/internal/table"
)

// The logs screen is a live tail, and everything here is shaped by the one
// rule a tail must obey: a poll that fails does not erase what is already on
// screen. It keeps the rows, says how old they are, and probes less often the
// longer the gateway stays unreachable. Blanking the table because one request
// timed out would claim the traffic stopped, which is the opposite of what
// happened.
//
// The three measurements are pointers on the wire because the gateway sends
// null for "not measured" — a request that failed before reaching a provider,
// or one served by a provider with no price in the catalog. They render as a
// dash. Collapsing null into 0 would draw a real zero-cost, zero-latency
// request, which is a claim no row here can support.

const (
	// logsRing is the display ring. A console that tails for a day must not
	// grow without limit, and 200 rows is more than the tallest pane can show.
	logsRing = 200

	// logsPoll is the tail cadence. A failure doubles it up to maxPoll and the
	// first success resets it.
	logsPoll = 2 * time.Second

	// logsPage is the default page size per poll.
	logsPage = 100

	// traceCell is the trace-id column: "tr_" plus eight bytes of the id, which
	// is what /admin/logs and the playground's attribution both key on.
	traceCell = 11
)

// keyRef is what a log row's api_key_id resolves to. Revoked is carried
// separately because a key revoked SINCE it served the traffic still names the
// rows it served — that is the whole reason keys have names.
type keyRef struct {
	Name    string
	Revoked bool
}

// logsScreen is the request-log pane: a filtered tail, a selection, and a
// detail strip.
type logsScreen struct {
	rows     []api.LogEntry // newest first, bounded at logsRing
	sel      int            // -1 when nothing is selected
	detail   bool
	follower *api.Follower
	filters  api.LogsQuery
	// raw is the filter arguments exactly as typed. The sub-line renders it
	// rather than reconstructing one from the parsed query, so what is on
	// screen is a line the operator could run again — "--since 15m", not the
	// "14m59s" a round trip through time.Duration turns it into.
	raw     string
	tailing bool

	// keyNames comes from one Keys() read at start. A caller without the
	// credential for it gets an empty map and raw ids, which is strictly better
	// than refusing to show the logs they can read.
	keyNames map[string]keyRef

	lastErr error
	staleAt time.Time // when the data went stale; zero while polls succeed
	backoff time.Duration
	// truncated records that the LAST poll hit the follower's page bound. The
	// rows it returned are real, so this is a readout beside them and not an
	// error: what is missing is the oldest end of that one window.
	truncated bool

	gen int
}

// ------------------------------------------------------------------ lifecycle

// start parses the filter arguments and opens the tail. A bad argument never
// enters the screen: the refusal belongs in the transcript, which is on the
// home pane, and switching away from it would hide the reason.
func (s *logsScreen) start(a *App, args string) tea.Cmd {
	q, err := parseLogFilters(args)
	if err != nil {
		a.Screen = ScreenHome
		a.Transcript.Push(TranscriptRow{Glyph: kindBad, Text: err.Error()})
		return nil
	}

	*s = logsScreen{gen: s.gen + 1, filters: q, raw: strings.TrimSpace(args),
		sel: -1, backoff: logsPoll, keyNames: map[string]keyRef{}}
	a.Screen = ScreenLogs
	if a.Client == nil {
		s.lastErr = errNoConnection
		return nil
	}
	s.tailing = true
	s.follower = api.NewFollower(a.Client, q)
	// No tick is armed here: the first poll arms the second. See rearm.
	return tea.Batch(fetchLogKeyNames(a.Client, s.gen), pollLogs(s.follower, s.gen))
}

// leave stops the tail. The generation bump is what actually stops it: a tick
// already in flight carries the old one and schedules nothing when it lands.
func (s *logsScreen) leave() {
	s.gen++
	s.tailing = false
	s.follower = nil
}

func (s *logsScreen) update(a *App, msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case tickLogsMsg:
		// Three independent reasons to stop: a newer visit, a pane that is no
		// longer on show, and a tail that was never started.
		if m.gen != s.gen || a.Screen != ScreenLogs || !s.tailing || s.follower == nil {
			return nil
		}
		return pollLogs(s.follower, s.gen)

	case logsBatchMsg:
		if m.gen != s.gen {
			return nil
		}
		// api.ErrFollowTruncated is the one error that arrives WITH rows: the
		// poll succeeded, the cursor moved past what it read, and what was lost
		// is the oldest end of that single window. Treating it as a failed poll
		// would discard rows the gateway did return and back the tail off
		// against a gateway whose only problem is that it is busy.
		if truncated := errors.Is(m.err, api.ErrFollowTruncated); m.err == nil || truncated {
			s.lastErr, s.staleAt, s.backoff = nil, time.Time{}, logsPoll
			s.truncated = truncated
			s.prepend(m.rows)
			return s.rearm()
		}
		s.lastErr = m.err
		if api.IsNotSupported(m.err) {
			// A gateway with no request-log store answers 501. That is an
			// absent feature, not a failed poll: it will not become a 200,
			// so the tail stops instead of backing off against it forever.
			s.tailing = false
			return nil
		}
		if s.staleAt.IsZero() {
			s.staleAt = time.Now()
		}
		s.backoff = backoff(s.backoff)
		return s.rearm()

	case logsNamesMsg:
		if m.gen != s.gen {
			return nil
		}
		s.keyNames = m.names
		return nil
	}
	return nil
}

// rearm schedules the next poll, and it is called from exactly one place: the
// arm that handles a finished one. That is what serialises the tail.
//
// api.Follower is one-per-tail-loop state — its cursor, its dedupe map and its
// row slice are unsynchronised — so two polls in flight against the same
// follower race, and a gateway whose /admin/logs takes longer than the poll
// interval is all it takes to have two. Arming the next tick only once the
// previous poll's rows have landed makes that unreachable. The generation
// counter cannot do this job: it discards a stale result after the fact, which
// is a different thing from never running two polls at once.
func (s *logsScreen) rearm() tea.Cmd {
	if !s.tailing || s.follower == nil {
		return nil
	}
	return tickLogs(s.backoff, s.gen)
}

// prepend puts a poll's ascending rows at the head, newest first, and trims to
// the ring. The selection follows its row rather than its index, so a batch
// arriving under the cursor does not silently re-point the detail strip at a
// different request.
func (s *logsScreen) prepend(rows []api.LogEntry) {
	if len(rows) == 0 {
		return
	}
	var selected string
	hadSelection := false
	if k, ok := s.row(); ok {
		selected = k.TraceID
		hadSelection = true
	}

	next := make([]api.LogEntry, 0, min(len(rows)+len(s.rows), logsRing))
	for i := len(rows) - 1; i >= 0; i-- {
		next = append(next, rows[i])
	}
	next = append(next, s.rows...)
	if len(next) > logsRing {
		next = next[:logsRing]
	}
	s.rows = next

	if !hadSelection {
		return
	}
	for i, r := range s.rows {
		if r.TraceID == selected {
			s.sel = i
			return
		}
	}
	s.sel, s.detail = -1, false
}

func (s *logsScreen) row() (api.LogEntry, bool) {
	if s.sel < 0 || s.sel >= len(s.rows) {
		return api.LogEntry{}, false
	}
	return s.rows[s.sel], true
}

// handleKey is the pane's own keyboard; see App.screenKey for why it is only
// ever offered a key while the command line is empty.
func (s *logsScreen) handleKey(key string) bool {
	switch key {
	case keyUp:
		s.sel = clampIdx(s.sel-1, len(s.rows))
	case keyDown:
		s.sel = clampIdx(s.sel+1, len(s.rows))
	case keyEnter:
		s.detail = s.sel >= 0 && s.sel < len(s.rows)
	case keyEsc:
		if !s.detail {
			return false
		}
		s.detail = false
	default:
		return false
	}
	return true
}

// -------------------------------------------------------------------- filters

// logFilterHelp is the vocabulary a refusal names. It is one string so the
// parser and its error message cannot drift apart.
const logFilterHelp = "filters are --since --model --provider --stage --key --limit"

// parseLogFilters walks `--flag value` pairs. An unknown flag is refused rather
// than ignored: a filter silently dropped shows a wider log than was asked for,
// and the operator has no way to tell.
func parseLogFilters(args string) (api.LogsQuery, error) {
	q := api.LogsQuery{Limit: logsPage}
	f := strings.Fields(args)
	for i := 0; i < len(f); i++ {
		name := f[i]
		if !strings.HasPrefix(name, "--") {
			return q, fmt.Errorf("logs: unexpected argument %q — %s", name, logFilterHelp)
		}
		if i+1 >= len(f) {
			return q, fmt.Errorf("logs: %s needs a value — %s", name, logFilterHelp)
		}
		v := f[i+1]
		i++
		switch name {
		case "--since":
			t, err := api.ParseSince(v)
			if err != nil {
				return q, fmt.Errorf("logs: %w — %s", err, logFilterHelp)
			}
			q.Since = t
		case "--model":
			q.Model = v
		case "--provider":
			q.Provider = v
		case "--stage":
			q.Stage = v
		case "--key":
			// "none" is the gateway's own sentinel for rows with no credential
			// and is passed through untouched.
			q.APIKeyID = v
		case "--limit":
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				return q, fmt.Errorf("logs: --limit %q is not a positive count — %s", v, logFilterHelp)
			}
			q.Limit = n
		default:
			return q, fmt.Errorf("logs: unknown flag %q — %s", name, logFilterHelp)
		}
	}
	return q, nil
}

// filterLabel renders the active filters the way they were typed, so what is
// on screen is a line the operator could run again.
func (s *logsScreen) filterLabel() string {
	if s.raw == "" {
		return "no filter · terminal rows only"
	}
	return strings.Join(strings.Fields(s.raw), " ")
}

// --------------------------------------------------------------------- view

// logColumns is the table. MODEL flexes because it is the only column whose
// content has no bound; everything else is a field of known shape.
func logColumns() []column {
	return []column{
		{head: "TIME", w: 8},
		{head: "TRACE", w: traceCell},
		{head: colProvider, w: 10},
		{head: "MODEL", w: 14, flex: true},
		{head: "STAGE", w: 13},
		{head: "MS", w: 6, right: true},
		{head: "COST", w: 8, right: true},
	}
}

func (s *logsScreen) view(a *App, w, rows int) string {
	th := a.Theme
	lines := []string{s.subLine(a, w)}

	// A 501 falls through to the empty note, which names the missing store. The
	// gateway's raw words are right for a failure and wrong for an absence.
	if s.lastErr != nil && len(s.rows) == 0 && !api.IsNotSupported(s.lastErr) {
		return fill(append(lines, "", th.Bad.Render("logs: "+s.lastErr.Error())), w, rows)
	}

	var strip []string
	if s.detail {
		if r, ok := s.row(); ok {
			strip = detailStrip(a, "Request "+r.TraceID, s.rowDetail(a, r), w)
		}
	}

	body := max(rows-len(lines)-len(strip), 0)
	cols := layout(logColumns(), w)
	if body > 0 {
		lines = append(lines, th.Dim.Render(headerLine(cols)))
		body--
	}
	if len(s.rows) == 0 {
		lines = append(lines, th.Dim.Render(s.emptyNote()))
	}
	for _, i := range window(len(s.rows), s.sel, body) {
		lines = append(lines, s.line(a, cols, i, w))
	}
	return fill(append(padTo(lines, rows-len(strip)), strip...), w, rows)
}

// emptyNote distinguishes "nothing has arrived yet" from "this gateway keeps no
// log": a tail against a gateway with no store would otherwise look identical
// to a quiet one.
func (s *logsScreen) emptyNote() string {
	switch {
	case s.lastErr != nil && api.IsNotSupported(s.lastErr):
		return "this gateway has no request-log store configured"
	case s.tailing:
		return "no rows yet — the tail is live and will fill as traffic arrives"
	default:
		return "no rows in range"
	}
}

// subLine carries the active filters on the left and the tail's own health on
// the right. Staleness is a readout, not an error: the rows below it are real,
// they are just older than they look.
func (s *logsScreen) subLine(a *App, w int) string {
	th := a.Theme
	right := ""
	switch {
	case !s.staleAt.IsZero():
		right = th.Warn.Render(fmt.Sprintf("stale %ds · retrying in %s",
			int(time.Since(s.staleAt).Seconds()), s.backoff))
	case s.truncated:
		// The rows below are real; the gateway was simply busy enough that one
		// poll could not reach the far end of its window. Saying which end was
		// cut is what stops the table reading as the whole range.
		right = th.Warn.Render("tailing · older rows in that window skipped")
	case s.tailing:
		right = th.Accent.Render("tailing…")
		if th.Mode.ASCII {
			right = th.Accent.Render("tailing...")
		}
	}
	return gapPad(w, " "+th.Dim.Render(s.filterLabel()), right)
}

func (s *logsScreen) line(a *App, cols []column, i, w int) string {
	th, r := a.Theme, s.rows[i]

	stage, stageStyle := r.Stage, th.OK
	if r.ErrorMessage != "" {
		stageStyle = th.Bad
	}
	line := rowLine(cols, []cell{
		{v: r.CreatedAt.Local().Format(time.TimeOnly), style: th.Dim},
		{v: shortTrace(r.TraceID), style: th.Accent},
		{v: table.OrDash(r.Provider), style: th.Text},
		{v: table.OrDash(r.Model), style: th.Bright},
		{v: table.OrDash(stage), style: stageStyle},
		{v: msCell(r.DurationMs), style: th.Text},
		{v: costCell(r.CostUSD), style: th.Dim},
	})
	if i == s.sel {
		return th.Selected.Render(padRight(line, w))
	}
	return line
}

// rowDetail is the strip. It carries the full trace id (the table's is cut),
// both measurements, and the credential resolved to a name — which is the one
// thing the table cannot show and the reason a row is opened at all.
func (s *logsScreen) rowDetail(_ *App, r api.LogEntry) []ModalKV {
	name, revoked := s.resolveKey(r.APIKeyID)
	out := []ModalKV{
		{K: "trace", V: r.TraceID, Bold: true},
		{K: "provider", V: table.OrDash(r.Provider)},
		{K: "model", V: table.OrDash(r.Model)},
		{K: "stage", V: table.OrDash(r.Stage)},
		{K: "duration", V: msUnit(r.DurationMs)},
		{K: "ttft", V: msUnit(r.TTFTMs)},
		{K: "tokens", V: fmt.Sprintf("%d in / %d out", r.PromptTokens, r.CompletionTokens)},
		{K: "cost", V: costCell(r.CostUSD)},
		{K: "api key", V: name, Warn: revoked},
	}
	if r.ErrorMessage != "" {
		out = append(out, ModalKV{K: "error", V: r.ErrorMessage, Warn: true})
	}
	return out
}

// resolveKey turns a row's api_key_id into something an operator can act on.
// Three cases, all real: the master credential carries a synthetic id with no
// key row behind it, a named key may have been revoked since it served the
// traffic, and a deleted key keeps its rows but loses its name.
func (s *logsScreen) resolveKey(id string) (string, bool) {
	switch {
	case id == "":
		return "none", false
	case strings.HasPrefix(id, "master-key:"):
		return "master key", false
	}
	if ref, ok := s.keyNames[id]; ok && ref.Name != "" {
		if ref.Revoked {
			return ref.Name + "  [revoked]", true
		}
		return ref.Name, false
	}
	return id, false
}

// ------------------------------------------------------------------ helpers

// shortTrace cuts a trace id to the table's column without inventing a shape:
// it keeps the head, which is what /admin/logs?trace= matches on.
func shortTrace(id string) string {
	if id == "" {
		return "-"
	}
	if len(id) <= traceCell {
		return id
	}
	return id[:traceCell]
}

// msCell and costCell render an absent measurement as a dash, never as a zero.
func msCell(v *float64) string {
	if v == nil {
		return "-"
	}
	return strconv.FormatFloat(*v, 'f', 0, 64)
}

func msUnit(v *float64) string {
	if v == nil {
		return "-"
	}
	return msCell(v) + "ms"
}

func costCell(v *float64) string {
	if v == nil {
		return "-"
	}
	return "$" + strconv.FormatFloat(*v, 'f', 4, 64)
}
