package tui

import (
	"context"
	"errors"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/ferro-labs/ai-gateway/cli/internal/api"
)

// This file is the shell's message vocabulary and the only place network I/O
// is started. A screen never calls the client from its view func: it returns a
// tea.Cmd built here, and the result arrives as one of the typed messages
// below. Screen-specific message types live alongside the shared poll results.
//
// Every poll result carries the generation it was requested under. The App
// drops a message whose gen no longer matches, so a slow in-flight response
// cannot overwrite state fetched against a newer profile.

// statusMsg is one /health + /readyz + /admin/health round trip. report and err
// are both meaningful: a failed poll annotates the header, it never blanks it.
type statusMsg struct {
	gen    int
	report *api.StatusReport
	err    error
}

// trafficMsg carries the 5-minute log-stats window the TRAFFIC panel derives
// RPS, P95 and the error rate from. A gateway with no log store answers 501,
// which is not an error state — stats is simply nil and the panel renders "—".
type trafficMsg struct {
	gen   int
	stats *api.LogStats
	err   error
}

// railMsg is the fan-in of the four rail sources (admin health, plugins,
// sessions, audit). Partial failure is normal and already encoded in RailData:
// AuthError for a 401, a -1 count for an endpoint this gateway does not serve.
type railMsg struct {
	gen  int
	data RailData
	err  error
}

// runCmdMsg is a composer submission. The shell forwards the raw line to the
// router in home.go, which is the only place a verb is interpreted.
type runCmdMsg struct{ raw string }

// verbRowsMsg is one finished transcript verb. rows and err are both
// meaningful: `status` renders a report even when the call failed, naming the
// URL it tried. took is measured around the request so the transcript can end
// with a duration the operator can trust.
type verbRowsMsg struct {
	verb string
	rows []TranscriptRow
	err  error
	took time.Duration
}

// The tick messages drive the two poll cadences. They carry no payload: the
// handler re-reads the current generation when it issues the next fetch.
type (
	tickStatusMsg  struct{}
	tickTrafficMsg struct{}
)

// errNoConnection is what a screen reports when it was opened with no client
// to ask. It is a state, not a failed request: nothing was attempted.
var errNoConnection = errors.New("no gateway connection configured")

// keysListMsg is one GET /admin/keys. Every row's `key` is masked by the
// gateway; nothing in this package ever holds an unmasked one from a read.
type keysListMsg struct {
	gen  int
	keys []api.Key
	err  error
}

// modalResultMsg is one finished key mutation.
//
// key carries the FULL secret for create and rotate — the only two responses
// that ever will. It goes straight into the modal that shows it once and is
// dropped when that modal closes. It is never pushed to the transcript, never
// recorded in history, and never logged. revoke has no row to answer with, so
// it carries the name and scope it was run against instead.
type modalResultMsg struct {
	gen         int
	kind        string // create | rotate | revoke
	key         *api.Key
	name, scope string
	err         error
}

// playModelsMsg is the playground's completion source and its default model.
// A failure is not surfaced: the pane still works, the gateway is simply the
// only authority on which names it accepts.
type playModelsMsg struct {
	gen    int
	models []string
	err    error
}

// chatOpenMsg is a stream that has begun. A non-2xx arriving BEFORE the stream
// starts is an error here; once streaming has begun the status is already 200
// and every failure arrives on the channel instead.
type chatOpenMsg struct {
	gen    int
	stream *api.ChatStream
	err    error
}

// The three in-stream messages. One event becomes one message and the handler
// re-issues the reader, which is the whole pump.
type (
	chatDeltaMsg struct {
		gen  int
		text string
	}
	chatUsageMsg struct {
		gen   int
		usage *api.Usage
	}
	// chatEndMsg is terminal, and its three shapes are distinct: an error
	// frame, a [DONE], or a channel that closed with neither — which is a
	// truncated answer and must not be mistaken for a clean finish.
	chatEndMsg struct {
		gen  int
		err  *api.Error
		done bool
	}
	// chatMetaMsg carries the request-log row that names the provider and the
	// cost, PLUS the turn, usage and elapsed time the fetch was issued for.
	// Those three are pinned by fetchAttribution's caller at issue time and
	// travel on the message rather than being resolved against screen state at
	// arrival, because a second turn's fetch can be issued before the first
	// turn's has landed (see playScreen.metaCmd) — carrying them here is what
	// lets the handler match each arriving message back to its own turn. row
	// is nil for every "cannot know" case and the meta line drops the segments
	// it cannot support.
	chatMetaMsg struct {
		gen   int
		turn  int
		usage *api.Usage
		took  time.Duration
		row   *api.LogEntry
	}
	flushTickMsg struct{ gen int }
)

func tickFlush(gen int) tea.Cmd {
	return tea.Tick(flushEvery, func(time.Time) tea.Msg { return flushTickMsg{gen: gen} })
}

// fetchChatModels lists what this gateway advertises. Its failure is swallowed
// into an empty list: a playground that refuses to open because /v1/models was
// unreachable is worse than one that cannot pre-validate a model name.
func fetchChatModels(c *api.Client, gen int) tea.Cmd {
	return func() tea.Msg {
		if c == nil {
			return playModelsMsg{gen: gen, err: errNoConnection}
		}
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
		models, err := c.Models(ctx)
		if err != nil {
			return playModelsMsg{gen: gen, err: err}
		}
		ids := make([]string, 0, len(models))
		for _, m := range models {
			ids = append(ids, m.ID)
		}
		return playModelsMsg{gen: gen, models: ids}
	}
}

// openChat starts one streamed completion. The context is deliberately NOT
// bounded by fetchTimeout: a long answer is not a stuck request, and the
// gateway already applies its own idle bound. ChatStream.Cancel is what ends it.
func openChat(ctx context.Context, c *api.Client, gen int, req api.ChatRequest) tea.Cmd {
	return func() tea.Msg {
		if c == nil {
			return chatOpenMsg{gen: gen, err: errNoConnection}
		}
		st, err := c.StreamChat(ctx, req)
		return chatOpenMsg{gen: gen, stream: st, err: err}
	}
}

// readStream turns the next event into a message. Err and Done are both
// terminal and mutually exclusive — a stream that fails mid-flight ends with an
// error frame and NO [DONE] — so this reads until the channel closes and treats
// a bare close as its own terminal case rather than waiting for a Done that is
// never coming.
func readStream(gen int, ev <-chan api.StreamEvent) tea.Cmd {
	return func() tea.Msg {
		e, ok := <-ev
		switch {
		case !ok:
			return chatEndMsg{gen: gen}
		case e.Err != nil:
			return chatEndMsg{gen: gen, err: e.Err}
		case e.Done:
			return chatEndMsg{gen: gen, done: true}
		case e.Chunk != nil && e.Chunk.Usage != nil:
			return chatUsageMsg{gen: gen, usage: e.Chunk.Usage}
		}
		var b strings.Builder
		if e.Chunk != nil {
			for _, c := range e.Chunk.Choices {
				b.WriteString(c.Delta.Content)
			}
		}
		return chatDeltaMsg{gen: gen, text: b.String()}
	}
}

// fetchAttribution resolves which provider served a streamed answer and what it
// cost. The log row is written after the stream ends, hence the one retry.
// Every failure answers a nil row: attribution is a detail beside an answer the
// operator already has, and failing over its absence would turn a cosmetic gap
// into a broken chat.
//
// turn, usage and took are the caller's pinned facts about the turn this fetch
// was issued for (see playScreen.metaCmd) — they are echoed back on every
// returned chatMetaMsg, success or failure alike, so the handler can match the
// message to its turn without consulting screen state that a second, later
// fetch may have since moved on from.
func fetchAttribution(c *api.Client, gen, turn int, usage *api.Usage, took time.Duration, traceID string, since time.Time) tea.Cmd {
	return func() tea.Msg {
		miss := chatMetaMsg{gen: gen, turn: turn, usage: usage, took: took}
		if c == nil || traceID == "" {
			return miss
		}
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
		for attempt := range 2 {
			if attempt > 0 {
				select {
				case <-ctx.Done():
					return miss
				case <-time.After(attributionRetry):
				}
			}
			row, err := c.TraceAttribution(ctx, traceID, since)
			if err != nil {
				return miss
			}
			if row != nil {
				miss.row = row
				return miss
			}
		}
		return miss
	}
}

// logsBatchMsg is one tail poll. rows and err are both meaningful: a failure
// annotates the table, it never empties it.
type logsBatchMsg struct {
	gen  int
	rows []api.LogEntry
	err  error
}

// logsNamesMsg resolves api_key_id → name for the detail strip. Its failure is
// swallowed into an empty map on purpose: a caller who can read the logs but
// not the key store still gets the logs, with raw ids.
type logsNamesMsg struct {
	gen   int
	names map[string]keyRef
}

// tickLogsMsg drives the tail. It carries the generation it was scheduled
// under, so a tick outliving its screen schedules nothing.
type tickLogsMsg struct{ gen int }

func tickLogs(d time.Duration, gen int) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return tickLogsMsg{gen: gen} })
}

func pollLogs(f *api.Follower, gen int) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
		rows, err := f.Poll(ctx)
		return logsBatchMsg{gen: gen, rows: rows, err: err}
	}
}

// fetchLogKeyNames reads the key store once per tail so the detail strip can
// name a credential. A key revoked since it served the traffic still names its
// rows — that is what key names are for — so the revoked flag travels with it.
func fetchLogKeyNames(c *api.Client, gen int) tea.Cmd {
	return func() tea.Msg {
		names := map[string]keyRef{}
		if c == nil {
			return logsNamesMsg{gen: gen, names: names}
		}
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
		keys, err := c.Keys(ctx)
		if err != nil {
			return logsNamesMsg{gen: gen, names: names} // raw ids beat no logs
		}
		for _, k := range keys {
			names[k.ID] = keyRef{Name: k.Name, Revoked: api.KeyState(k) == api.KeyStateRevoked}
		}
		return logsNamesMsg{gen: gen, names: names}
	}
}

func fetchKeys(c *api.Client, gen int) tea.Cmd {
	return func() tea.Msg {
		if c == nil {
			return keysListMsg{gen: gen, err: errNoConnection}
		}
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
		keys, err := c.Keys(ctx)
		return keysListMsg{gen: gen, keys: keys, err: err}
	}
}

func createKey(c *api.Client, gen int, req api.KeyCreateRequest) tea.Cmd {
	return func() tea.Msg {
		if c == nil {
			return modalResultMsg{gen: gen, kind: actionCreate, err: errNoConnection}
		}
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
		k, err := c.CreateKey(ctx, req)
		return modalResultMsg{gen: gen, kind: actionCreate, key: k, err: err}
	}
}

func rotateKey(c *api.Client, gen int, id string) tea.Cmd {
	return func() tea.Msg {
		if c == nil {
			return modalResultMsg{gen: gen, kind: actionRotate, err: errNoConnection}
		}
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
		k, err := c.RotateKey(ctx, id)
		return modalResultMsg{gen: gen, kind: actionRotate, key: k, err: err}
	}
}

func revokeKey(c *api.Client, gen int, id, name, scope string) tea.Cmd {
	return func() tea.Msg {
		if c == nil {
			return modalResultMsg{gen: gen, kind: actionRevoke, err: errNoConnection}
		}
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
		// RevokeKey answers with no row: the gateway confirms {"status":
		// "revoked"} and the refreshed listing is what shows the new state.
		err := c.RevokeKey(ctx, id)
		return modalResultMsg{gen: gen, kind: actionRevoke, name: name, scope: scope, err: err}
	}
}

// Poll cadences and the failure backoff ceiling. A failing poll doubles its
// interval up to maxPoll and resets on the first success, so an unreachable
// gateway is not hammered every 5s while its stale-data age keeps climbing.
const (
	statusPoll  = 5 * time.Second
	trafficPoll = 10 * time.Second
	maxPoll     = 30 * time.Second

	// fetchTimeout bounds one poll. It is longer than the client's own 15s
	// request timeout would need to be for a single call because a rail fetch
	// makes four.
	fetchTimeout = 20 * time.Second

	// trafficWindow is the log-stats window; RPS divides by its seconds.
	trafficWindow = 5 * time.Minute

	// auditRows is one transcript page of the audit trail. The gateway caps at
	// 200; the transcript ring is 60, so asking for more only drops rows.
	auditRows = 50
)

func fetchStatus(c *api.Client, gen int) tea.Cmd {
	return func() tea.Msg {
		if c == nil {
			return statusMsg{gen: gen, err: errNoConnection}
		}
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
		r, err := c.Status(ctx)
		return statusMsg{gen: gen, report: r, err: err}
	}
}

func fetchTraffic(c *api.Client, gen int) tea.Cmd {
	return func() tea.Msg {
		if c == nil {
			return trafficMsg{gen: gen, err: errNoConnection}
		}
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
		s, err := c.LogStats(ctx, api.LogsQuery{Since: time.Now().Add(-trafficWindow)})
		if api.IsNotSupported(err) {
			// No log store configured. Not an error state — just no numbers.
			return trafficMsg{gen: gen}
		}
		return trafficMsg{gen: gen, stats: s, err: err}
	}
}

// fetchRail collects the left rail in one command. Each source degrades on its
// own: an unsupported endpoint leaves its count at -1 ("—") and a credential
// problem sets AuthError, because the rail is exactly where an operator should
// see that ferro is connected but not authorized.
//
// prev is the rail's last good snapshot. Each of the four sources overwrites
// only ITS OWN slice/counters when it succeeds; a source that fails this round
// leaves prev's values for it untouched rather than zeroing them, so one
// flaky source no longer blanks CONNECTED PROVIDERS or MCP just because the
// other three answered fine. The caller still learns the round had a problem
// via railMsg.err — this only stops a transient failure from being drawn as
// "nothing to report" instead of "the last good answer, aging".
func fetchRail(c *api.Client, gen int, prev RailData) tea.Cmd {
	return func() tea.Msg {
		d := prev
		// AuthError is a verdict about THIS round, not a running tally: it is
		// re-earned every poll, or a since-fixed 401 would stay stuck on
		// screen after the credential is corrected.
		d.AuthError = false
		if c == nil {
			return railMsg{gen: gen, data: d, err: errNoConnection}
		}
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
		var firstErr error
		// note reports whether this source failed. A 404/501 is not a failure —
		// it is a gateway that does not serve that endpoint, which leaves the
		// counter at unknownCount and renders as a dash.
		note := func(err error) bool {
			if err == nil {
				return false
			}
			if api.IsUnauthorized(err) {
				d.AuthError = true
			}
			if firstErr == nil && !api.IsNotSupported(err) {
				firstErr = err
			}
			return true
		}

		type result struct {
			kind     string
			health   *api.AdminHealth
			plugins  []api.PluginInfo
			sessions []api.Session
			audit    *api.AuditPage
			err      error
		}
		results := make(chan result, 4)
		go func() {
			value, err := c.AdminHealth(ctx)
			results <- result{kind: "health", health: value, err: err}
		}()
		go func() {
			value, err := c.Plugins(ctx)
			results <- result{kind: verbPlugins, plugins: value, err: err}
		}()
		go func() {
			value, err := c.Sessions(ctx)
			results <- result{kind: verbSessions, sessions: value, err: err}
		}()
		since := time.Now()
		since = time.Date(since.Year(), since.Month(), since.Day(), 0, 0, 0, 0, since.Location())
		go func() {
			value, err := c.Audit(ctx, api.AuditQuery{Since: since, Limit: 1})
			results <- result{kind: verbAudit, audit: value, err: err}
		}()

		for range 4 {
			r := <-results
			if note(r.err) {
				continue
			}
			switch r.kind {
			case "health":
				d.Providers = r.health.Providers
				// Recount from this round's body instead of adding to the last
				// one's. d starts as prev so a failed source keeps its previous
				// value, which makes every ++ here a running total unless the
				// counter is cleared first — two good polls would report twice
				// the MCP servers that exist. Providers above overwrites, and
				// PluginsActive below clears for this same reason.
				d.MCPTotal, d.MCPReady = 0, 0
				for _, s := range r.health.MCPServers {
					d.MCPTotal++
					if s.Ready {
						d.MCPReady++
					}
				}
			case verbPlugins:
				d.PluginsActive = 0
				for _, p := range r.plugins {
					if p.Enabled {
						d.PluginsActive++
					}
				}
			case verbSessions:
				d.Sessions = len(r.sessions)
			case verbAudit:
				d.AuditToday = r.audit.Summary.TotalEntries
			}
		}
		return railMsg{gen: gen, data: d, err: firstErr}
	}
}

// verbFetch runs one transcript verb off the render path. Timing lives here,
// where the clock is; formatting the duration and the failure into rows is the
// update arm's job, which keeps it pure and testable.
func verbFetch(c *api.Client, verb string) tea.Cmd {
	return func() tea.Msg {
		start := time.Now()
		if c == nil {
			return verbRowsMsg{verb: verb, err: errNoConnection, took: time.Since(start)}
		}
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
		rows, err := verbRows(ctx, c, verb)
		return verbRowsMsg{verb: verb, rows: rows, err: err, took: time.Since(start)}
	}
}

func tickStatus(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return tickStatusMsg{} })
}

func tickTraffic(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return tickTrafficMsg{} })
}
