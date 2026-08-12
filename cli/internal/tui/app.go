// Package tui is ferro's full-screen operations console.
//
// One root model (App) owns navigation, polling and layout; each screen is a
// plain struct with pure update/view funcs, so only this file touches the
// Bubble Tea interface and a v2 API surprise is contained to one adapter.
//
// Two rules make the whole package testable and honest:
//
//   - Update delegates to the unexported update, which mutates the App and
//     returns a command. Tests call it directly — no terminal, no program loop.
//   - Nothing here performs I/O from a view func. Every request lives in a
//     tea.Cmd in msgs.go and comes back as a typed message.
//
// Measurements are in terminal cells (lipgloss.Width), never bytes: a frame
// padded with len() breaks the first time a CJK model name or a styled glyph
// arrives.
package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"

	"github.com/ferro-labs/ai-gateway/cli/internal/api"
	"github.com/ferro-labs/ai-gateway/cli/internal/config"
	"github.com/ferro-labs/ai-gateway/cli/internal/tui/theme"
	"github.com/ferro-labs/ai-gateway/cli/internal/version"
)

// Screen is the pane the main frame is currently showing.
type Screen int

// The screens the console can show. Each owns a pane; the shell owns the
// header, rail, composer, and polling.
const (
	ScreenHome Screen = iota
	ScreenLogs
	ScreenKeys
	ScreenPlayground
)

// Responsive breakpoints and the fixed rail geometry.
// Never render a border that cannot close within the measured width.
const (
	wideMin   = 110 // mark + left rail + panels
	mediumMin = 80  // compact brand + STATUS frame
	railWidth = 34
	railGap   = 2

	// minSplitWidth is the narrowest main frame that can carry the
	// TARGETS│TRAFFIC split header with both titles and a closing rule.
	minSplitWidth = 30

	// providerRows caps the rail's provider list; the remainder collapses
	// into a "+N more" line rather than pushing SERVICES off screen.
	providerRows = 6

	// unknownCount marks a rail counter this gateway does not serve (404/501)
	// or would not answer without a credential. It renders as a dash, never 0.
	unknownCount = -1

	// emDash is the one honest answer for a value that does not exist on the
	// wire. There is no gateway version over HTTP, so the header prints this.
	emDash = "—"
)

// The connection states the header reads. The first two are the gateway's own
// words, matched against what /health reported; the third is ours, for a
// gateway that reported nothing at all.
const (
	stateConnected   = "connected"
	stateDegraded    = "degraded"
	stateUnreachable = "unreachable"
)

// The provider states that count as working. Anything else earns the warning
// glyph, so a status this console has never heard of is shown as a concern
// rather than silently as health.
const (
	statusHealthy   = "healthy"
	statusAvailable = "available"
)

// RailData is the left rail's four sources, already reduced to what it draws.
// A count of unknownCount means "not served", which is not the same as zero.
type RailData struct {
	Providers          []api.AdminProviderHealth // nil when unauthorized
	MCPReady, MCPTotal int
	PluginsActive      int
	Sessions           int
	AuditToday         int
	AuthError          bool
}

// App is the root model. Every field the screens read lives here; the polling
// bookkeeping below the line stays unexported.
type App struct {
	Client  *api.Client
	Profile config.Resolved
	Theme   theme.Theme

	Width, Height int
	Screen        Screen

	// Composer is the console's only navigation surface; Transcript is what
	// the home screen shows. Both are values: the App owns them outright.
	Composer   Composer
	Transcript Transcript

	// The three screens below Home. Each is a value the App owns outright and
	// each carries its own generation counter, so a message from a screen the
	// operator has left is dropped rather than applied — the same contract
	// pollGen gives the shell's own polls.
	Logs logsScreen
	Keys keysScreen
	Play playScreen

	// Modal is the console's one overlay. It renders in place of the pane and
	// takes every key while it is open, which is what makes a typed
	// confirmation a confirmation rather than a race with the command line.
	Modal *Modal

	// Status is the last GOOD report. A failed poll sets StatusErr and leaves
	// this alone, so one dropped refresh never blanks the header — it ages it.
	Status    *api.StatusReport
	StatusErr error
	StatusAt  time.Time

	Traffic    *api.LogStats // nil → the TRAFFIC panel renders "—"
	TrafficErr error

	Rail    RailData
	RailErr error

	// pollGen is bumped when the connection changes; a message carrying an
	// older generation is dropped, so a slow in-flight response cannot
	// overwrite state fetched against the newer profile.
	pollGen int

	statusEvery, trafficEvery time.Duration
}

var _ tea.Model = (*App)(nil)

func newApp(client *api.Client, profile config.Resolved, mode theme.Mode) *App {
	return &App{
		Client:  client,
		Profile: profile,
		Theme:   theme.New(mode),
		// Replaced by the first WindowSizeMsg; a sane default keeps the very
		// first frame from drawing at 0 cells.
		Width:        100,
		Height:       30,
		Screen:       ScreenHome,
		Rail:         RailData{PluginsActive: unknownCount, Sessions: unknownCount, AuditToday: unknownCount},
		Composer:     newComposer(Verbs(nil)),
		statusEvery:  statusPoll,
		trafficEvery: trafficPoll,
	}
}

// Run starts the console. The caller has already established that stdout is a
// terminal — a bare `ferro` on a pipe must never draw this.
//
// root is the caller's own cobra tree, and the composer's vocabulary is walked
// out of it: the console completes exactly what the CLI can run. It is passed
// in rather than built here because internal/command is what hands bare `ferro`
// to this function, so importing it back would be a cycle.
func Run(client *api.Client, profile config.Resolved, mode theme.Mode, root *cobra.Command) error {
	a := newApp(client, profile, mode)
	a.Composer = newComposer(Verbs(root))
	_, err := tea.NewProgram(a).Run()
	return err
}

// Init starts the polling loops. Part of tea.Model.
func (a *App) Init() tea.Cmd {
	return tea.Batch(
		fetchStatus(a.Client, a.pollGen),
		fetchRail(a.Client, a.pollGen, a.Rail),
		fetchTraffic(a.Client, a.pollGen),
		tickStatus(a.statusEvery),
		tickTraffic(a.trafficEvery),
	)
}

// Update delegates to update, which mutates a and returns a command. Tests
// drive that directly, with no terminal. Part of tea.Model.
func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) { return a, a.update(msg) }

// View is the only adapter that knows about tea.View. Everything else — every
// screen, every test — deals in strings.
func (a *App) View() tea.View {
	v := tea.NewView(a.render())
	v.AltScreen = true
	return v
}

func (a *App) update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		a.Width, a.Height = m.Width, m.Height
		// The playground lays its markdown out ahead of the view, so it is the
		// one screen that has to be told the pane changed size.
		a.Play.resize(a.paneWidth())
		return nil

	case tea.KeyPressMsg:
		key := m.String()
		if key == keyCtrlC {
			a.Play.leave()
			a.Logs.leave()
			a.Keys.leave()
			return tea.Quit
		}
		if a.Modal != nil {
			// An open modal owns the keyboard outright. A confirmation whose
			// keys also reach the command line is not a confirmation.
			return a.Modal.handle(key, a)
		}
		prev := a.Screen
		if a.screenKey(key) {
			return nil
		}
		// The composer owns every other key, esc included: in reverse search
		// esc abandons the search, and only outside it does esc mean "back".
		return a.left(prev, a.Composer.handle(key, a))

	case runCmdMsg:
		prev := a.Screen
		return a.left(prev, a.route(m.raw))

	case keysListMsg, modalResultMsg:
		return a.Keys.update(a, msg)

	case logsBatchMsg, logsNamesMsg, tickLogsMsg:
		return a.Logs.update(a, msg)

	case playModelsMsg, chatOpenMsg, chatDeltaMsg, chatUsageMsg, chatEndMsg, chatMetaMsg, flushTickMsg:
		return a.Play.update(a, msg)

	case verbRowsMsg:
		a.Transcript.Push(m.rows...)
		if m.err != nil {
			// The gateway's own words, not a paraphrase: an api.Error already
			// says what failed and often what to do about it.
			a.Transcript.Push(TranscriptRow{Glyph: kindBad, Text: m.err.Error()})
		}
		a.Transcript.Push(TranscriptRow{
			Text: fmt.Sprintf("Completed in %dms", m.took.Milliseconds()),
			Dim:  true,
		})
		return nil

	case statusMsg:
		if m.gen != a.pollGen {
			return nil
		}
		if m.err != nil {
			a.StatusErr = m.err
			a.statusEvery = backoff(a.statusEvery)
			// api.Status returns a report alongside its error so the caller can
			// still name the URL it tried. Adopt it only when nothing better is
			// held — a good report is never replaced by a failure's.
			if a.Status == nil && m.report != nil {
				a.Status, a.StatusAt = m.report, time.Now()
			}
			return nil
		}
		a.Status, a.StatusErr, a.StatusAt = m.report, nil, time.Now()
		a.statusEvery = statusPoll
		return nil

	case trafficMsg:
		if m.gen != a.pollGen {
			return nil
		}
		if m.err != nil {
			a.TrafficErr = m.err
			a.trafficEvery = backoff(a.trafficEvery)
			return nil
		}
		a.Traffic, a.TrafficErr = m.stats, nil
		a.trafficEvery = trafficPoll
		return nil

	case railMsg:
		if m.gen != a.pollGen {
			return nil
		}
		a.Rail, a.RailErr = m.data, m.err
		return nil

	case tickStatusMsg:
		return tea.Batch(fetchStatus(a.Client, a.pollGen), fetchRail(a.Client, a.pollGen, a.Rail), tickStatus(a.statusEvery))

	case tickTrafficMsg:
		return tea.Batch(fetchTraffic(a.Client, a.pollGen), tickTraffic(a.trafficEvery))
	}
	return nil
}

// backoff doubles a poll interval, capped, so an unreachable gateway is probed
// less often the longer it stays down. A success resets it at the call site.
func backoff(d time.Duration) time.Duration {
	if d *= 2; d > maxPoll {
		return maxPoll
	}
	return d
}

// ---------------------------------------------------------------- rendering

// render composes the whole screen. It is a pure function of App state: no
// network, no clock beyond the stale-age readout.
func (a *App) render() string {
	wide := a.Width >= wideMin
	medium := !wide && a.Width >= mediumMin

	var header string
	if wide && a.Screen == ScreenHome {
		header = a.viewBrandBlock()
	} else {
		header = a.compactBrand()
	}

	mainW := a.Width
	if wide {
		mainW = a.Width - railWidth - railGap
	}

	// Line budget: header + blank + body + composer + hints(1). The composer
	// is measured rather than assumed: it grows by two lines when it has
	// suggestions to show, and the pane must give those lines up so the header
	// does not scroll off the top of the screen.
	composer := a.composerView()
	fixed := lipgloss.Height(header) + 2 + lipgloss.Height(composer)
	overhead := 2 // a plain frame's top and bottom borders
	if wide || medium {
		overhead = 7 // top + 3 stat rows + divider + pane title + bottom
	}
	if medium {
		fixed += 6 // the STATUS frame and its trailing newline
	}
	rows := max(a.Height-fixed-overhead, 1)

	main := a.mainFrame(mainW, wide || medium, rows)

	var b strings.Builder
	b.WriteString(header)
	b.WriteString("\n\n")
	switch {
	case wide:
		rail := lipgloss.JoinVertical(lipgloss.Left,
			a.Theme.Frame("CONNECTED PROVIDERS", a.viewProviders(), railWidth),
			a.Theme.Frame("SERVICES", a.viewServices(), railWidth))
		b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, rail, strings.Repeat(" ", railGap), main))
	case medium:
		b.WriteString(a.Theme.Frame("STATUS", a.viewCompactStatus(), a.Width))
		b.WriteString("\n")
		b.WriteString(main)
	default:
		b.WriteString(main)
	}
	b.WriteString("\n")
	b.WriteString(composer)
	b.WriteString("\n")
	b.WriteString(a.viewHints())
	return b.String()
}

// viewBrandBlock is the wide home header: the split-F mark beside the text
// stack. Every value in it is real — see stateLine and originLine.
func (a *App) viewBrandBlock() string {
	stackW := max(a.Width-lipgloss.Width(theme.Mark(a.Theme))-3, 10)
	stack := strings.Join(clampLines([]string{
		a.brand() + " " + a.Theme.Dim.Render(version.Version),
		a.Theme.Border.Render(a.rule(10)),
		a.Theme.Text.Render("AI Gateway"),
		a.stateLine(),
		a.Theme.Dim.Render(a.originLine()),
	}, stackW), "\n")
	return lipgloss.JoinHorizontal(lipgloss.Top, theme.Mark(a.Theme), "   ", stack)
}

// compactBrand is the one-line header every other width and screen gets: the
// artwork collapses before any operational data does.
func (a *App) compactBrand() string {
	line := a.brand() + " " + a.Theme.Dim.Render(version.Version) + "  " + a.stateLine()
	return ansi.Truncate(line, a.Width, "")
}

// brand is the wordmark. Bold is still an ANSI escape, so it is gated on the
// same Color switch NO_COLOR and a non-TTY stdout turn off.
func (a *App) brand() string {
	return a.Theme.Bright.Bold(a.Theme.Mode.Color).Render("Ferro Labs")
}

// stateLine is the connection readout: glyph and colour from the report's own
// state, then profile, URL and the client-measured round trip. When the last
// poll failed the line keeps its data and gains a staleness age.
func (a *App) stateLine() string {
	g := a.Theme.Mode.Glyphs()
	glyph, label, style := g.None, "connecting", a.Theme.Dim
	if a.Status != nil {
		switch a.Status.State {
		case stateConnected:
			glyph, label, style = g.Dot, stateConnected, a.Theme.OK
		case stateDegraded:
			glyph, label, style = g.Warn, stateDegraded, a.Theme.Warn
		default:
			glyph, label, style = g.Bad, a.Status.State, a.Theme.Bad
		}
	} else if a.StatusErr != nil {
		glyph, label, style = g.Bad, stateUnreachable, a.Theme.Bad
	}

	parts := []string{}
	if a.Profile.ProfileName != "" {
		parts = append(parts, a.Profile.ProfileName)
	}
	parts = append(parts, a.url())
	if a.Status != nil && a.Status.LatencyMs > 0 {
		parts = append(parts, fmt.Sprintf("%dms", a.Status.LatencyMs))
	}
	if a.StatusErr != nil && !a.StatusAt.IsZero() {
		parts = append(parts, fmt.Sprintf("stale %ds", int(time.Since(a.StatusAt).Seconds())))
	} else if a.StatusErr != nil {
		parts = append(parts, "stale")
	}
	return style.Render(glyph+" "+label) + a.Theme.Dim.Render(" · "+strings.Join(parts, " · "))
}

// originLine is the counts row. The gateway serves no version over HTTP, so
// that segment is a dash — never a guess.
func (a *App) originLine() string {
	parts := []string{"gateway " + a.dash()}
	if a.Status != nil {
		// A gateway that did not report targets gets a dash. /readyz answers a
		// 503 carrying only {status, reason}, and "0 targets" there would read
		// as "none configured" during exactly the outage this header is read in.
		if t := a.Status.Targets; t != nil {
			parts = append(parts, fmt.Sprintf("%d targets", t.Total))
		} else {
			parts = append(parts, a.dash()+" targets")
		}
		if n := a.Status.Models; n != nil {
			parts = append(parts, comma(*n)+" models")
		}
	}
	return strings.Join(parts, " · ")
}

func (a *App) url() string {
	if a.Status != nil && a.Status.URL != "" {
		return a.Status.URL
	}
	return a.Profile.URL
}

// ------------------------------------------------------------------- panels

// mainFrame draws the console's main frame. With panels it carries the split
// header layout that theme.Frame cannot express:
//
//	┌─ TARGETS ──────────┬─ TRAFFIC ──────────┐
//	│ Healthy       7    │ RPS         159.0  │
//	├────────────────────┴────────────────────┤
//	│ COMMAND OUTPUT                          │
//	└─────────────────────────────────────────┘
//
// This is the one frame drawn outside the theme; everything else goes through
// it. Below minSplitWidth the split cannot close, so the pane falls back to a
// plain titled frame.
func (a *App) mainFrame(w int, panels bool, rows int) string {
	if !panels || w < minSplitWidth {
		return a.Theme.Frame(a.paneTitle(), a.paneView(w-4, rows), w)
	}
	b := a.boxChars()
	bd := a.Theme.Border
	lw := (w - 3) / 2
	rw := w - 3 - lw

	out := make([]string, 0, rows+7)
	out = append(out, bd.Render(b.tl+b.h)+" "+a.Theme.Dim.Render("TARGETS")+" "+
		bd.Render(strings.Repeat(b.h, lw-10)+b.tj+b.h)+" "+a.Theme.Dim.Render("TRAFFIC")+" "+
		bd.Render(strings.Repeat(b.h, rw-10)+b.tr))

	targets, traffic := a.viewTargets(), a.viewTraffic()
	edge := bd.Render(b.v)
	for i := range 3 {
		out = append(out, edge+" "+padRight(targets[i], lw-2)+" "+edge+" "+padRight(traffic[i], rw-2)+" "+edge)
	}
	out = append(out, bd.Render(b.lj+strings.Repeat(b.h, lw)+b.bj+strings.Repeat(b.h, rw)+b.rj))

	body := append([]string{a.Theme.Bright.Render(a.paneTitle())}, strings.Split(a.paneView(w-4, rows), "\n")...)
	for _, line := range body {
		out = append(out, edge+" "+padRight(line, w-4)+" "+edge)
	}
	out = append(out, bd.Render(b.bl+strings.Repeat(b.h, w-2)+b.br))
	return strings.Join(out, "\n")
}

// viewTargets is the TARGETS panel: routable targets, and the two circuit
// states that explain the difference. No report → dashes, never zeros.
func (a *App) viewTargets() [3]string {
	if a.Status == nil {
		d := a.dash()
		return [3]string{
			padRight("Healthy", 14) + d,
			padRight("Degraded", 14) + d,
			padRight("Open circuit", 14) + d,
		}
	}
	warn := func(n int, style lipgloss.Style) string {
		if n > 0 {
			return style.Render(strconv.Itoa(n))
		}
		return strconv.Itoa(n)
	}
	// Circuits are reported by /health, which answers even when /readyz is a
	// bare 503 — so they stay real while the target counts go absent.
	healthy := a.Theme.Dim.Render(a.dash())
	if t := a.Status.Targets; t != nil {
		healthy = a.Theme.OK.Render(strconv.Itoa(t.Routable))
	}
	return [3]string{
		padRight("Healthy", 14) + healthy,
		padRight("Degraded", 14) + warn(a.Status.Circuits.HalfOpen, a.Theme.Warn),
		padRight("Open circuit", 14) + warn(a.Status.Circuits.Open, a.Theme.Bad),
	}
}

// viewTraffic derives the panel from one /admin/logs/stats window. Every
// unknown renders as a dash: a gateway with no log store answers 501, which is
// an absence of data, not a zero and not an error state.
func (a *App) viewTraffic() [3]string {
	rps, p95, errRate := a.dash(), a.dash(), a.dash()
	if t := a.Traffic; t != nil {
		rps = fmt.Sprintf("%.1f", float64(t.Summary.TotalEntries)/trafficWindow.Seconds())
		if t.LatencyMs != nil {
			p95 = fmt.Sprintf("%.0fms", t.LatencyMs.P95)
		}
		if t.Summary.TotalEntries > 0 {
			errRate = fmt.Sprintf("%.2f%%", 100*float64(t.Summary.ErrorEntries)/float64(t.Summary.TotalEntries))
		}
	}
	// 7 cells, not 5: a p95 of 1284ms truncated to "1284m" does not read as a
	// clipped number, it reads as minutes.
	return [3]string{
		padRight("RPS", 14) + padLeft(rps, 7),
		padRight("P95", 14) + padLeft(p95, 7),
		padRight("Errors", 14) + padLeft(errRate, 7),
	}
}

// --------------------------------------------------------------------- rail

// viewProviders is the CONNECTED PROVIDERS frame body. There is no per-provider
// latency on the wire, so the right column is the model count the gateway
// actually reports.
func (a *App) viewProviders() string {
	inner := railWidth - 4
	g := a.Theme.Mode.Glyphs()
	if a.Rail.AuthError {
		return a.Theme.Dim.Render(padRight("auth required "+a.dash()+" set FERRO_API_KEY", inner))
	}
	if len(a.Rail.Providers) == 0 {
		return a.Theme.Dim.Render(padRight("no providers reported", inner))
	}
	shown := min(len(a.Rail.Providers), providerRows)
	lines := make([]string, 0, shown+1)
	for _, p := range a.Rail.Providers[:shown] {
		glyph := a.Theme.OK.Render(g.Dot)
		if p.Status != statusHealthy && p.Status != statusAvailable {
			glyph = a.Theme.Warn.Render(g.Warn)
		}
		lines = append(lines, gapPad(inner, glyph+" "+p.Name, a.Theme.Dim.Render(comma(p.Models)+" models")))
	}
	if rest := len(a.Rail.Providers) - shown; rest > 0 {
		lines = append(lines, a.Theme.Dim.Render(fmt.Sprintf("+%d more", rest)))
	}
	return strings.Join(lines, "\n")
}

// viewServices is the SERVICES frame body: the four readiness counters the
// mock keeps permanently on screen.
func (a *App) viewServices() string {
	mcp := a.dash()
	style := a.Theme.Text
	if a.Rail.MCPTotal > 0 {
		mcp = fmt.Sprintf("%d/%d ready", a.Rail.MCPReady, a.Rail.MCPTotal)
		if a.Rail.MCPReady < a.Rail.MCPTotal {
			style = a.Theme.Warn
		} else {
			style = a.Theme.OK
		}
	}
	// Label column 16 + the longest value ("N events today") is exactly the
	// rail's 30 inner cells; widening it truncates the audit row.
	return strings.Join([]string{
		padRight("MCP servers", 16) + style.Render(mcp),
		padRight("Plugins", 16) + a.count(a.Rail.PluginsActive, "active", a.Theme.OK),
		padRight("Sessions", 16) + a.count(a.Rail.Sessions, "operators", a.Theme.Text),
		padRight("Audit", 16) + a.count(a.Rail.AuditToday, "events today", a.Theme.Dim),
	}, "\n")
}

// count renders a rail counter, or a dash when the gateway does not serve it.
func (a *App) count(n int, unit string, style lipgloss.Style) string {
	if n == unknownCount {
		return a.Theme.Dim.Render(a.dash())
	}
	return style.Render(fmt.Sprintf("%d %s", n, unit))
}

// viewCompactStatus is the medium-width collapse of the rail plus the header
// details the one-line brand cannot carry.
func (a *App) viewCompactStatus() string {
	// "gateway <dash>" is the header's phrasing; here the row label already
	// says Gateway, so the dash stands alone rather than repeating the word.
	gateway, targets, providers := a.dash(), a.dash(), a.dash()
	if a.Status != nil {
		if a.Status.LatencyMs > 0 {
			gateway += fmt.Sprintf(" · %dms", a.Status.LatencyMs)
		}
		routable := a.dash() + "/" + a.dash()
		if t := a.Status.Targets; t != nil {
			routable = fmt.Sprintf("%d/%d", t.Routable, t.Total)
		}
		targets = fmt.Sprintf("%s routable · %d degraded · %d open",
			routable, a.Status.Circuits.HalfOpen, a.Status.Circuits.Open)
		// A count the gateway supplied is shown as given, zero included; one it
		// never supplied leaves the dash these start as.
		if n := a.Status.Providers; n != nil {
			providers = strconv.Itoa(*n)
		}
		if n := a.Status.Models; n != nil {
			providers += " · " + comma(*n) + " models"
		}
	}
	services := fmt.Sprintf("MCP %s · plugins %s · sessions %s · audit %s",
		a.mcpRatio(), plainCount(a.Rail.PluginsActive, a.dash()),
		plainCount(a.Rail.Sessions, a.dash()), plainCount(a.Rail.AuditToday, a.dash()))
	if a.Rail.AuthError {
		services = "auth required " + a.dash() + " set FERRO_API_KEY"
	}
	inner := a.Width - 4
	return strings.Join(clampLines([]string{
		padRight("Gateway", 12) + a.Theme.Dim.Render(gateway),
		padRight("Targets", 12) + a.Theme.Text.Render(targets),
		padRight("Providers", 12) + a.Theme.Text.Render(providers),
		padRight("Services", 12) + a.Theme.Dim.Render(services),
	}, inner), "\n")
}

func (a *App) mcpRatio() string {
	if a.Rail.MCPTotal == 0 {
		return a.dash()
	}
	return fmt.Sprintf("%d/%d", a.Rail.MCPReady, a.Rail.MCPTotal)
}

func plainCount(n int, dash string) string {
	if n == unknownCount {
		return dash
	}
	return strconv.Itoa(n)
}

// --------------------------------------------------------- pane + composer

func (a *App) paneTitle() string {
	switch a.Screen {
	case ScreenLogs:
		return "LOGS"
	case ScreenKeys:
		return "KEYS"
	case ScreenPlayground:
		return "PLAYGROUND"
	default:
		return "COMMAND OUTPUT"
	}
}

// paneView dispatches to the current screen's body. Every branch returns
// exactly rows lines, at most w cells wide, so the composer stays anchored to
// the bottom of the screen whatever is on show above it.
//
// A modal renders IN PLACE of the pane rather than over it. There is nothing
// to dim behind an overlay in a terminal, and the pane's exact-line contract is
// the one thing an overlay would have to break.
func (a *App) paneView(w, rows int) string {
	if a.Modal != nil {
		return a.Modal.view(a, w, rows)
	}
	switch a.Screen {
	case ScreenLogs:
		return a.Logs.view(a, w, rows)
	case ScreenKeys:
		return a.Keys.view(a, w, rows)
	case ScreenPlayground:
		return a.Play.view(a, w, rows)
	default:
		return a.homeView(w, rows)
	}
}

// paneWidth is the width paneView will next be called with, derived exactly as
// render() derives it. A screen that lays content out ahead of the view — the
// playground wraps markdown when a delta batch flushes, not per frame — needs
// the number outside a view func.
func (a *App) paneWidth() int {
	w := a.Width
	if w >= wideMin {
		w -= railWidth + railGap
	}
	return max(w-4, 1)
}

// screenKey offers a key to the pane before the composer sees it and reports
// whether the pane took it.
//
// The pane is only offered a key while the command line is EMPTY. The composer
// is the console's only navigation surface, so typing `keys` from the logs
// screen has to outrank moving a cursor — which is also why the selection keys
// are ↑/↓ and not j/k: `k` is the first letter of a verb, and a screen that
// swallowed it would make that verb untypeable from this pane.
func (a *App) screenKey(key string) bool {
	if a.Composer.searchMode || a.Composer.Input != "" {
		return false
	}
	switch a.Screen {
	case ScreenLogs:
		return a.Logs.handleKey(key)
	case ScreenKeys:
		return a.Keys.handleKey(key)
	}
	return false
}

// left cancels the screen the shell has just moved off. Every path that can
// change a.Screen funnels through here — the router, and the composer's own
// esc — so a tail keeps ticking and a stream keeps streaming for exactly as
// long as its pane is on show, and not one message longer.
func (a *App) left(prev Screen, cmd tea.Cmd) tea.Cmd {
	if a.Screen == prev {
		return cmd
	}
	switch prev {
	case ScreenLogs:
		a.Logs.leave()
	case ScreenKeys:
		a.Keys.leave()
	case ScreenPlayground:
		a.Play.leave()
	}
	return cmd
}

func (a *App) composerView() string { return a.Composer.view(a) }

func (a *App) viewHints() string {
	left, right := "↵ run   tab complete   ↑ history   ctrl+r search", "? help   ctrl+c quit"
	if a.Theme.Mode.ASCII {
		left = "enter run   tab complete   up history   ctrl+r search"
	}
	return gapPad(a.Width, " "+a.Theme.Dim.Render(left), a.Theme.Dim.Render(right))
}

// ------------------------------------------------------------------ helpers

// boxChars mirrors the theme's border vocabulary with the two junctions the
// split header needs; theme.Frame draws no junctions, so it exports none.
type boxChars struct{ tl, tr, bl, br, h, v, tj, bj, lj, rj string }

func (a *App) boxChars() boxChars {
	if a.Theme.Mode.ASCII {
		return boxChars{"+", "+", "+", "+", "-", "|", "+", "+", "+", "+"}
	}
	return boxChars{"┌", "┐", "└", "┘", "─", "│", "┬", "┴", "├", "┤"}
}

func (a *App) dash() string {
	if a.Theme.Mode.ASCII {
		return "-"
	}
	return emDash
}

func (a *App) rule(n int) string {
	if a.Theme.Mode.ASCII {
		return strings.Repeat("-", n)
	}
	return strings.Repeat("─", n)
}

// padRight pads or truncates to exactly w cells. ANSI-aware in both directions:
// measuring ignores escape sequences and truncation keeps them intact.
func padRight(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = ansi.Truncate(s, w, "")
	if p := w - lipgloss.Width(s); p > 0 {
		s += strings.Repeat(" ", p)
	}
	return s
}

func padLeft(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = ansi.Truncate(s, w, "")
	if p := w - lipgloss.Width(s); p > 0 {
		s = strings.Repeat(" ", p) + s
	}
	return s
}

// gapPad lays left and right out at the two ends of exactly w cells, giving up
// the left side first when they cannot both fit.
func gapPad(w int, left, right string) string {
	if w <= 0 {
		return ""
	}
	right = ansi.Truncate(right, w, "")
	rw := lipgloss.Width(right)
	left = ansi.Truncate(left, max(w-rw-1, 0), "")
	return left + strings.Repeat(" ", max(w-lipgloss.Width(left)-rw, 0)) + right
}

func clampLines(lines []string, w int) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = ansi.Truncate(l, w, "")
	}
	return out
}

// comma groups thousands. 2500 models reads as a quantity; 2500 reads as an id.
func comma(n int) string {
	s := strconv.Itoa(n)
	sign := ""
	if strings.HasPrefix(s, "-") {
		sign, s = "-", s[1:]
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return sign + s
}
