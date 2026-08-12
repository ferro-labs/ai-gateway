package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/ferro-labs/ai-gateway/cli/internal/api"
	"github.com/ferro-labs/ai-gateway/cli/internal/table"
	"github.com/ferro-labs/ai-gateway/cli/internal/version"
)

// This file is the home screen and the command router — the two halves of
// "typing a verb does something". The router is the console's whole navigation
// model, so it reads as one table on purpose.
//
// Nothing here performs I/O. A verb that needs the gateway returns the tea.Cmd
// from msgs.go and its rows arrive later as a verbRowsMsg.

// The verbs more than one place has to agree on. A fetched verb is named twice
// — route() dispatches it and verbRows() answers it — and a spelling that
// matches in one but not the other is an "unknown command" or a "has no
// fetcher" that neither file can see on its own. The console-only verbs are
// named for the same reason: the composer offers them (tuiVerbs), the router
// runs them, and the playground has to recognise them to tell a command from a
// prompt. `services` is handled in exactly one place and stays a literal there;
// there is nothing for it to drift from.
const (
	verbHelp       = "help"
	verbClear      = "clear"
	verbVersion    = "version"
	verbModel      = "model"
	verbChat       = "chat"
	verbPlayground = "playground"

	verbStatus    = "status"
	verbModels    = "models"
	verbProviders = "providers"
	verbMCP       = "mcp"
	verbPlugins   = "plugins"
	verbSessions  = "sessions"
	verbAudit     = "audit"

	verbLogs = "logs"
	verbKeys = "keys"
)

// Column headings more than one table draws. The same fact is spelled the same
// way everywhere — NAME is the object's own name, STATE the condition, PROVIDER
// the target that served it — so an operator who has read one table can read the
// next. A heading only one table has stays a literal at its call site.
const (
	colName     = "NAME"
	colState    = "STATE"
	colProvider = "PROVIDER"
)

// homeView is the transcript, padded to exactly rows lines.
func (a *App) homeView(w, rows int) string {
	if a.Transcript.Len() == 0 {
		return fill([]string{"", a.Theme.Dim.Render("No output yet. Type a command below, or ? for help.")}, w, rows)
	}
	return a.Transcript.view(a.Theme, w, rows)
}

// route is the dispatch table. Every branch either renders into the transcript
// or switches screens; none of them touches the network directly.
func (a *App) route(raw string) tea.Cmd {
	line := strings.TrimSpace(raw)
	if line == "" {
		return nil
	}
	// A slash action is the same verb: /logs and logs are one command. Only the
	// verb is case-folded — an argument like --model Claude-* is a glob, and
	// lowercasing it would quietly change which rows match.
	line = strings.TrimPrefix(line, "/")
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return nil
	}
	head := strings.ToLower(fields[0])
	rest := strings.Join(fields[1:], " ")

	// The playground's seam. On that screen a line that does not name a verb is
	// a prompt, not a mistyped command — and the test cannot key on the slash,
	// which the composer strips before this function is ever reached. It sits
	// here, ahead of the echo, because a prompt is not a command and must not be
	// written into the transcript as one. `line` is still exactly what was
	// typed, case and all, which is what a prompt needs.
	if a.Screen == ScreenPlayground {
		if cmd, claimed := a.Play.route(a, head, rest, line); claimed {
			return cmd
		}
	}

	line = strings.TrimSpace(head + " " + rest)

	if head == verbClear {
		a.Transcript.Reset()
		a.Screen = ScreenHome
		return nil
	}

	echo := line
	if rest != "" && (head == verbChat || head == verbPlayground) {
		echo = head
	}
	a.Transcript.Push(TranscriptRow{Glyph: kindCmd, Text: "ferro " + echo})

	switch head {
	case verbHelp, "?":
		a.Screen = ScreenHome
		a.Transcript.Push(helpRows(a.Composer.verbs)...)
		return nil

	case verbVersion:
		a.Screen = ScreenHome
		a.Transcript.Push(TranscriptRow{Glyph: kindOK, Text: "ferro " + version.String()})
		return nil

	// Screens own their own data. The router only moves to them; Tasks 14–16
	// wire the fetch, so starting one here would be output with nowhere to go.
	case verbLogs:
		return a.Logs.start(a, rest)
	case verbKeys:
		return a.Keys.enter(a, rest)
	case verbPlayground, verbChat:
		return a.Play.enter(a, rest)
	case "services":
		a.Screen = ScreenHome
		for _, line := range strings.Split(a.viewServices(), "\n") {
			a.Transcript.Push(TranscriptRow{Text: line})
		}
		return nil

	case verbStatus, verbModels, verbProviders, verbMCP, verbPlugins, verbSessions, verbAudit:
		a.Screen = ScreenHome
		return verbFetch(a.Client, head)
	}

	a.Screen = ScreenHome
	a.Transcript.Push(TranscriptRow{
		Glyph: kindBad,
		Text:  fmt.Sprintf("unknown command %q — press ? for the verb tree", head),
	})
	return nil
}

// helpRows prints the verb tree from the composer's own vocabulary, which came
// from the cobra tree. Hand-listing it here is how a help screen starts naming
// verbs that no longer exist.
func helpRows(verbs []string) []TranscriptRow {
	var top, nested, slash []string
	for _, v := range verbs {
		switch {
		case strings.HasPrefix(v, "/"):
			slash = append(slash, v)
		case strings.Contains(v, " "):
			nested = append(nested, v)
		default:
			top = append(top, v)
		}
	}
	rows := []TranscriptRow{{Glyph: kindNote, Text: strings.Join(top, "   ")}}
	if len(nested) > 0 {
		rows = append(rows, TranscriptRow{Text: strings.Join(nested, "   "), Dim: true})
	}
	return append(rows,
		TranscriptRow{Text: "slash actions: " + strings.Join(slash, " "), Dim: true},
		TranscriptRow{Text: "every verb has a scriptable twin — run it with --format json for a pipe", Dim: true},
	)
}

// ------------------------------------------------------------ verb rendering

// verbRows fetches one verb and renders it as transcript rows. It is called
// from the tea.Cmd in msgs.go, never from a view func.
func verbRows(ctx context.Context, c *api.Client, verb string) ([]TranscriptRow, error) {
	switch verb {
	case verbStatus:
		// Status returns a report even when it fails, naming the URL it tried;
		// rendering it is what makes state:"unreachable" visible.
		r, err := c.Status(ctx)
		if r == nil {
			return nil, err
		}
		mcp := "-"
		if r.MCP != nil {
			mcp = fmt.Sprintf("%d/%d", r.MCP.Ready, r.MCP.Total)
		}
		// Targets is nil when the gateway did not report them — a 503 /readyz
		// carrying only {status, reason}. "0/0" there would claim none are
		// configured when the truth is that they are dead.
		targets := "-"
		if t := r.Targets; t != nil {
			targets = fmt.Sprintf("%d/%d", t.Routable, t.Total)
		}
		return tableRows(
			[]string{colState, "URL", "LATENCY", "TARGETS", "PROVIDERS", "MODELS", "MCP", "AUTH"},
			[][]string{{
				r.State, r.URL, fmt.Sprintf("%dms", r.LatencyMs), targets,
				table.CountOrDash(r.Providers), table.CountOrDash(r.Models), mcp, table.OrDash(r.Auth),
			}}), err

	case verbModels:
		models, err := c.Models(ctx)
		if err != nil {
			return nil, err
		}
		cells := make([][]string, 0, len(models))
		for _, m := range models {
			cells = append(cells, []string{
				m.ID, table.OrDash(m.OwnedBy), table.OrDash(m.Mode), table.PositiveOrDash(m.ContextWindow),
				fmt.Sprintf("%d", len(m.Capabilities)), table.OrDash(m.Status),
			})
		}
		return orEmpty(tableRows([]string{"ID", "OWNED BY", "MODE", "CONTEXT", "CAPABILITIES", "STATUS"}, cells),
			len(cells), "this gateway routes no models"), nil

	case verbProviders:
		health, _, err := c.Health(ctx)
		if err != nil {
			return nil, err
		}
		// Best effort: an unusable credential costs two columns, not the verb.
		admin, adminErr := c.AdminHealth(ctx)
		if adminErr != nil {
			admin = nil
		}
		cells := providerCells(health, admin)
		rows := tableRows([]string{colProvider, "STATUS", "CIRCUIT", "MODELS", "MESSAGE"}, cells)
		if adminErr != nil {
			// Name what actually happened. A refused credential and a timed-out
			// or broken /admin/health cost the same two columns, but only one of
			// them is fixed by looking at a credential.
			why := "admin health unavailable"
			if api.IsUnauthorized(adminErr) {
				why = "no admin credential accepted"
			}
			rows = append(rows, TranscriptRow{Glyph: kindWarn,
				Text: why + " — status and message come from /health only"})
		}
		return orEmpty(rows, len(cells), "no providers reported"), nil

	case verbMCP:
		servers, err := mcpServers(ctx, c)
		if err != nil {
			return nil, err
		}
		cells := make([][]string, 0, len(servers))
		for _, s := range servers {
			cells = append(cells, []string{s.Name, table.BoolYN(s.Ready), table.BoolYN(s.Required), table.OrDash(s.LastError)})
		}
		return orEmpty(tableRows([]string{colName, "READY", "REQUIRED", "LAST ERROR"}, cells),
			len(cells), "this gateway has no MCP servers configured"), nil

	case verbPlugins:
		configured, err := c.Plugins(ctx)
		if err != nil {
			return nil, err
		}
		catalog, err := c.PluginCatalog(ctx)
		if err != nil {
			// A build without the catalog route still lists what it runs; it
			// just cannot say what fails open.
			if !api.IsNotSupported(err) {
				return nil, err
			}
			catalog = nil
		}
		cells := pluginCells(configured, catalog)
		return orEmpty(tableRows([]string{colName, "TYPE", "ENABLED", "FAILS", "SUMMARY"}, cells),
			len(cells), "this gateway has no plugins configured"), nil

	case verbSessions:
		sessions, err := c.Sessions(ctx)
		if err != nil {
			return nil, err
		}
		cells := make([][]string, 0, len(sessions))
		for _, s := range sessions {
			cells = append(cells, []string{
				s.Subject, strings.Join(s.Scopes, ","),
				table.FmtTime(s.CreatedAt), table.FmtTimePtr(s.LastSeenAt), table.FmtTime(s.ExpiresAt),
			})
		}
		return orEmpty(tableRows([]string{"SUBJECT", "SCOPES", "CREATED", "LAST SEEN", "EXPIRES"}, cells),
			len(cells), "no active dashboard sessions"), nil

	case verbAudit:
		page, err := c.Audit(ctx, api.AuditQuery{Limit: auditRows})
		if err != nil {
			return nil, err
		}
		cells := make([][]string, 0, len(page.Data))
		for _, e := range page.Data {
			cells = append(cells, []string{
				table.FmtTime(e.OccurredAt), e.Action, table.OrDash(e.Actor), e.Outcome, table.OrDash(e.TargetID),
			})
		}
		return orEmpty(tableRows([]string{"TIME", "ACTION", "ACTOR", "OUTCOME", "TARGET"}, cells),
			len(cells), "no audit entries in range"), nil
	}
	return nil, fmt.Errorf("verb %q has no fetcher", verb)
}

// mcpServers prefers /admin/health: it alone carries last_error, which /readyz
// withholds because it is unauthenticated and the reason can quote a URL.
func mcpServers(ctx context.Context, c *api.Client) ([]api.MCPServer, error) {
	if ah, err := c.AdminHealth(ctx); err == nil {
		return ah.MCPServers, nil
	}
	ready, _, err := c.Ready(ctx)
	if err != nil {
		return nil, err
	}
	return ready.MCPServers, nil
}

// providerCells merges the two listings: /health is unauthenticated and carries
// the circuit; /admin/health needs a credential and carries the status, the
// model count and the failure message.
func providerCells(h *api.HealthReport, ah *api.AdminHealth) [][]string {
	circuits := make(map[string]string, len(h.Providers))
	for _, p := range h.Providers {
		circuits[p.Name] = p.Circuit
	}
	if ah == nil {
		cells := make([][]string, 0, len(h.Providers))
		for _, p := range h.Providers {
			cells = append(cells, []string{p.Name, p.Status, table.OrDash(p.Circuit), fmt.Sprintf("%d", p.Models), "-"})
		}
		return cells
	}
	cells := make([][]string, 0, len(ah.Providers))
	for _, p := range ah.Providers {
		cells = append(cells, []string{
			p.Name, p.Status, table.OrDash(circuits[p.Name]), fmt.Sprintf("%d", p.Models), table.OrDash(p.Message),
		})
	}
	return cells
}

// The two failure policies a catalog entry reports. Which one a plugin has is
// the single most consequential thing on the plugins table — it is the
// difference between a broken guardrail stopping traffic and waving it through
// — so the column says it in the catalog's own words.
// pluginCells merges what this gateway has configured with what this build
// ships — only the catalog knows whether a plugin fails open, and the
// scriptable table derives that cell from the same place.
func pluginCells(configured []api.PluginInfo, catalog []api.BuiltinPlugin) [][]string {
	shipped := table.NewPluginCatalog(catalog)
	cells := make([][]string, 0, len(configured))
	for _, p := range configured {
		policy := shipped.For(p.Name)
		cells = append(cells, []string{
			p.Name, p.Type, table.BoolYN(p.Enabled), policy.Fails, table.OrDash(policy.Summary),
		})
	}
	return cells
}

// ------------------------------------------------------------------ helpers

// tableRows renders headers and cells through internal/table's shared layout
// — the same one Printer.Table (internal/command/output.go) calls — so a verb
// read in the console and the same verb read through a pipe align identically.
// This is the only thing left here: the TranscriptRow wrapping, dimming the
// header and its rule because they are furniture, leaving the data alone.
func tableRows(headers []string, cells [][]string) []TranscriptRow {
	lines := table.Rows(headers, cells)
	rows := make([]TranscriptRow, 0, len(lines))
	for i, line := range lines {
		rows = append(rows, TranscriptRow{Text: line, Dim: i < 2})
	}
	return rows
}

// orEmpty replaces a table with nothing in it by a row that says so. An empty
// gateway and a broken query look identical otherwise.
func orEmpty(rows []TranscriptRow, n int, empty string) []TranscriptRow {
	if n > 0 {
		return rows
	}
	return []TranscriptRow{{Text: empty, Dim: true}}
}

// fill pads a body to exactly rows lines, truncating each to w cells.
func fill(lines []string, w, rows int) string {
	out := append(make([]string, 0, rows), clampLines(lines, w)...)
	for len(out) < rows {
		out = append(out, "")
	}
	return strings.Join(out[:max(rows, 0)], "\n")
}
