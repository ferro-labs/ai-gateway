package tui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/ferro-labs/ai-gateway/cli/internal/api"
	"github.com/ferro-labs/ai-gateway/cli/internal/tui/theme"
)

// TableRows hands tableRows to the external test package. The layout is held
// equal to internal/command's Printer.Table there (verbs_cobra_test.go),
// because that assertion needs to import internal/command and this package
// never may.
var TableRows = tableRows

// transcriptText flattens the transcript for assertions that do not care where
// a row's text sits on screen.
func transcriptText(a *App) string {
	var b strings.Builder
	for _, r := range a.Transcript.rows {
		b.WriteString(r.Glyph + " " + r.Text + "\n")
	}
	return b.String()
}

func lastRow(a *App) TranscriptRow {
	if n := len(a.Transcript.rows); n > 0 {
		return a.Transcript.rows[n-1]
	}
	return TranscriptRow{}
}

func TestRouteEchoesEveryCommand(t *testing.T) {
	a := composerApp()
	a.route("status")
	if got := a.Transcript.rows[0]; got.Glyph != "$" || got.Text != "ferro status" {
		t.Fatalf("every command must be echoed as it would be run, got %#v", got)
	}
}

func TestRouteUnknownVerb(t *testing.T) {
	a := composerApp()
	if cmd := a.route("frobnicate --hard"); cmd != nil {
		t.Fatal("an unknown verb must not reach the network")
	}
	row := lastRow(a)
	if row.Glyph != "bad" || !strings.Contains(row.Text, `"frobnicate"`) {
		t.Fatalf("an unknown verb must be named in a red row, got %#v", row)
	}
	if !strings.Contains(row.Text, "?") {
		t.Fatalf("the refusal must point at the verb tree, got %q", row.Text)
	}
	if a.Screen != ScreenHome {
		t.Fatal("an unknown verb leaves the operator on the transcript")
	}
}

func TestRouteHelpListsVerbs(t *testing.T) {
	for _, raw := range []string{"help", "?"} {
		a := composerApp()
		a.route(raw)
		text := transcriptText(a)
		for _, want := range []string{"status", "logs", "playground", "keys", "keys create", "/providers"} {
			if !strings.Contains(text, want) {
				t.Fatalf("%q must list %q:\n%s", raw, want, text)
			}
		}
		if !strings.Contains(text, "--format json") {
			t.Fatalf("help must point at the scriptable twin:\n%s", text)
		}
	}
}

func TestRouteStatusEchoesAndFetches(t *testing.T) {
	a := composerApp()
	cmd := a.route("status")
	if cmd == nil {
		t.Fatal("status must return a fetch command")
	}
	if !strings.Contains(transcriptText(a), "ferro status") {
		t.Fatalf("status must echo its scriptable form:\n%s", transcriptText(a))
	}
	if a.Screen != ScreenHome {
		t.Fatal("status renders into the transcript, so it must land on home")
	}
	// The command must run without a client rather than panicking on nil.
	msg, ok := cmd().(verbRowsMsg)
	if !ok || msg.err == nil {
		t.Fatalf("a verb fetched with no connection must report the failure, got %#v", msg)
	}
}

func TestRouteSwitchesScreens(t *testing.T) {
	for raw, want := range map[string]Screen{
		"logs --model claude-*": ScreenLogs,
		"logs":                  ScreenLogs,
		"keys":                  ScreenKeys,
		"keys create":           ScreenKeys,
		"playground":            ScreenPlayground,
		"chat":                  ScreenPlayground,
	} {
		a := composerApp()
		a.route(raw)
		if a.Screen != want {
			t.Fatalf("%q must switch to screen %v, got %v", raw, want, a.Screen)
		}
	}
}

func TestRouteClearResetsTranscriptAndScreen(t *testing.T) {
	a := composerApp()
	a.route("status")
	a.Screen = ScreenLogs
	a.route("clear")
	if a.Transcript.Len() != 0 {
		t.Fatalf("clear must empty the transcript, got %d rows", a.Transcript.Len())
	}
	if a.Screen != ScreenHome {
		t.Fatal("clear returns to the transcript")
	}
}

func TestRouteStripsSlashAndIgnoresVerbCase(t *testing.T) {
	a := composerApp()
	a.route("/STATUS")
	if !strings.Contains(transcriptText(a), "ferro status") {
		t.Fatalf("a slash action is the same verb, lowercased:\n%s", transcriptText(a))
	}
	a = composerApp()
	a.route("  ")
	if a.Transcript.Len() != 0 {
		t.Fatal("a blank line is not a command")
	}
	a.route("/")
	if a.Transcript.Len() != 0 {
		t.Fatal("a bare slash is not a command")
	}
}

func TestInlineChatPromptIsNotEchoedToTranscript(t *testing.T) {
	a := composerApp()
	a.route("chat SENTINEL_SECRET_DO_NOT_STORE")
	if strings.Contains(transcriptText(a), "SENTINEL_SECRET_DO_NOT_STORE") {
		t.Fatalf("chat prompt leaked into transcript:\n%s", transcriptText(a))
	}
}

// Arguments keep their case: a --model filter is a glob, not a verb.
func TestRouteKeepsArgumentCase(t *testing.T) {
	a := composerApp()
	a.route("logs --model Claude-Sonnet")
	if !strings.Contains(transcriptText(a), "Claude-Sonnet") {
		t.Fatalf("arguments must survive verb normalisation:\n%s", transcriptText(a))
	}
}

func TestVerbRowsLandInTranscript(t *testing.T) {
	a := composerApp()
	a.update(verbRowsMsg{
		verb: "status",
		rows: []TranscriptRow{{Glyph: "ok", Text: "STATE  URL"}, {Glyph: "ok", Text: "connected  http://x"}},
		took: 41 * time.Millisecond,
	})
	text := transcriptText(a)
	if !strings.Contains(text, "connected  http://x") {
		t.Fatalf("fetched rows must land in the transcript:\n%s", text)
	}
	if !strings.Contains(text, "Completed in 41ms") {
		t.Fatalf("every async verb must report its duration:\n%s", text)
	}
	if !lastRow(a).Dim {
		t.Fatal("the duration row is dim furniture, not output")
	}
}

func TestVerbFailureAppendsARedRow(t *testing.T) {
	a := composerApp()
	err := &api.Error{Status: 401, Message: "missing credential"}
	a.update(verbRowsMsg{verb: "keys", err: err, took: time.Millisecond})

	var found bool
	for _, r := range a.Transcript.rows {
		if r.Glyph == "bad" && strings.Contains(r.Text, err.Error()) {
			found = true
		}
	}
	if !found {
		t.Fatalf("a failed verb must carry the gateway's own error text:\n%s", transcriptText(a))
	}
}

// The pane is the shell's anchor: exactly rows lines on every screen, so the
// composer never moves.
func TestPaneIsExactlyRowsLinesOnEveryScreen(t *testing.T) {
	a := composerApp()
	a.route("status")
	for _, screen := range []Screen{ScreenHome, ScreenLogs, ScreenKeys, ScreenPlayground} {
		a.Screen = screen
		for _, rows := range []int{1, 5, 18} {
			body := a.paneView(60, rows)
			if got := len(strings.Split(body, "\n")); got != rows {
				t.Fatalf("screen %v pane at rows=%d returned %d lines", screen, rows, got)
			}
			for i, line := range strings.Split(body, "\n") {
				if lw := lipgloss.Width(line); lw > 60 {
					t.Fatalf("screen %v pane line %d overflows: %d cells %q", screen, i, lw, line)
				}
			}
		}
	}
}

// Every screen now says something. An empty pane would read as "this gateway
// has nothing to show", which is a claim none of them can support before their
// first fetch lands.
func TestEveryScreenSaysSomethingBeforeItsFirstFetch(t *testing.T) {
	a := composerApp() // no client
	for raw, want := range map[string]string{
		// With nothing to ask, the pane names the reason rather than looking
		// like a gateway with no logs and no keys.
		"logs": errNoConnection.Error(),
		"keys": errNoConnection.Error(),
		// The playground needs no fetch to accept a prompt; the failure
		// surfaces on the turn, where it can be read.
		"playground": "Type a prompt",
	} {
		a.route(raw)
		if body := a.paneView(70, 8); !strings.Contains(body, want) {
			t.Fatalf("%q must say %q rather than render blank:\n%s", raw, want, body)
		}
	}

	// And a real, genuinely empty listing says THAT, which is a different fact.
	a = composerApp()
	a.route("logs")
	a.Logs.lastErr = nil
	if body := a.paneView(70, 8); !strings.Contains(body, "no rows") {
		t.Fatalf("an empty log window must say so:\n%s", body)
	}
	a.route("keys")
	a.Keys.err = nil
	if body := a.paneView(70, 8); !strings.Contains(body, "no API keys") {
		t.Fatalf("a gateway with no keys must say so:\n%s", body)
	}
}

// The whole shell, with a transcript in it, still fits every width and mode.
func TestRenderWithTranscriptFitsEveryWidth(t *testing.T) {
	for _, mode := range []theme.Mode{{}, {Color: true}, {ASCII: true}, {Color: true, ASCII: true}} {
		for _, w := range []int{72, 96, 126} {
			a := composerApp()
			a.Theme, a.Width = theme.New(mode), w
			a.route("help")
			a.update(verbRowsMsg{verb: "status", rows: []TranscriptRow{
				{Glyph: "ok", Text: strings.Repeat("a very wide row ", 20)},
			}, took: 12 * time.Millisecond})
			for i, line := range strings.Split(a.render(), "\n") {
				if lw := lipgloss.Width(line); lw > w {
					t.Fatalf("mode=%+v w=%d line %d overflows: %d cells %q", mode, w, i, lw, line)
				}
			}
		}
	}
}

// stubGateway serves just enough of the admin API for the transcript verbs.
func stubGateway(t *testing.T) *api.Client {
	t.Helper()
	mux := http.NewServeMux()
	body := func(path, json string) {
		mux.HandleFunc(path, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, json)
		})
	}
	body("/health", `{"status":"ok","providers":[
		{"name":"openai","status":"healthy","circuit":"closed","models":1104},
		{"name":"anthropic","status":"degraded","circuit":"half_open","models":412}]}`)
	body("/readyz", `{"status":"ready","targets":[{"name":"openai","routable":true}],
		"mcp_servers":[{"name":"filesystem","ready":true,"required":false}]}`)
	body("/admin/health", `{"status":"ok","scopes":["admin"],"providers":[
		{"name":"openai","status":"healthy","models":1104},
		{"name":"anthropic","status":"degraded","models":412,"message":"rate limited upstream"}],
		"mcp_servers":[{"name":"filesystem","ready":true,"required":true},
		{"name":"search","ready":false,"required":false,"last_error":"handshake timeout"}]}`)
	body("/v1/models", `{"object":"list","data":[
		{"id":"claude-sonnet-4-6","owned_by":"anthropic","mode":"chat","context_window":200000,
		 "capabilities":["chat","tools"],"status":"available"}]}`)
	body("/admin/plugins", `[{"name":"rate-limit","type":"ratelimit","enabled":true},
		{"name":"request-logger","type":"logging","enabled":false}]`)
	body("/admin/plugins/catalog", `{"data":[{"name":"rate-limit","type":"ratelimit","summary":"caps request rate","fails_open":false},
		{"name":"request-logger","type":"logging","summary":"records requests","fails_open":true}]}`)
	body("/admin/sessions", `{"data":[{"id":"s1","subject":"alice","scopes":["admin"],
		"created_at":"2026-08-08T09:00:00Z","expires_at":"2026-08-09T09:00:00Z"}]}`)
	body("/admin/audit", `{"data":[{"occurred_at":"2026-08-08T09:00:00Z","action":"key.create",
		"actor":"alice","outcome":"ok","target_id":"ak_1"}],"summary":{"total_entries":1,"returned_entries":1}}`)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c, err := api.New(srv.URL, "test-key")
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	return c
}

// mustLocalRFC3339 renders the UTC wire timestamp the sessions and audit
// fixtures carry (stubGateway) the way verbRows actually renders it: through
// fmtTime, in the reader's local time. Asserting this rather than a literal
// RFC3339 string keeps the test honest about what the column shows without
// hardcoding a timezone-dependent value — the repo convention is to assert
// UTC for persisted/admin timestamps, but this cell is deliberately local.
func mustLocalRFC3339(t *testing.T, utc string) string {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, utc)
	if err != nil {
		t.Fatalf("bad fixture timestamp %q: %v", utc, err)
	}
	return parsed.Local().Format(time.RFC3339)
}

// The transcript's verbs are the scriptable verbs: same sources, same columns,
// same alignment. This is the test that fails when one of them drifts.
func TestVerbRowsRenderEveryTranscriptVerb(t *testing.T) {
	c := stubGateway(t)
	// Both fixtures (stubGateway) carry this same UTC instant as their
	// administrative timestamp; verbRows renders it through fmtTime, which
	// converts to local time (home.go). Without this, a regression in that
	// conversion — or in which field feeds the column — passes silently.
	localCreatedAt := mustLocalRFC3339(t, "2026-08-08T09:00:00Z")
	for verb, want := range map[string][]string{
		// One provider's circuit is half-open, so the report is degraded — the
		// derivation lives in api.Status and the transcript must not soften it.
		"status":    {"STATE", "URL", "LATENCY", "degraded", "1/1", "admin"},
		"models":    {"ID", "OWNED BY", "claude-sonnet-4-6", "anthropic", "200000"},
		"providers": {"PROVIDER", "CIRCUIT", "openai", "closed", "half_open", "rate limited upstream"},
		"mcp":       {"NAME", "READY", "REQUIRED", "filesystem", "yes", "handshake timeout"},
		"plugins":   {"NAME", "FAILS", "rate-limit", "closed", "request-logger", "open"},
		"sessions":  {"SUBJECT", "SCOPES", "alice", "admin", localCreatedAt},
		"audit":     {"ACTION", "OUTCOME", "key.create", "ok", "ak_1", localCreatedAt},
	} {
		rows, err := verbRows(t.Context(), c, verb)
		if err != nil {
			t.Fatalf("%s: %v", verb, err)
		}
		var text strings.Builder
		for _, r := range rows {
			text.WriteString(r.Text + "\n")
		}
		for _, w := range want {
			if !strings.Contains(text.String(), w) {
				t.Fatalf("%s output missing %q:\n%s", verb, w, text.String())
			}
		}
		// The header and its rule are furniture, so they are dim; the data is
		// the output and must not be.
		if len(rows) < 3 || !rows[0].Dim || !rows[1].Dim || rows[2].Dim {
			t.Fatalf("%s: header must be dim and data must not be: %#v", verb, rows[:min(3, len(rows))])
		}
	}
}

// A gateway that does not serve an endpoint must surface its own words, not a
// paraphrase and not an empty table.
func TestVerbRowsSurfaceGatewayErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"missing credential","code":"unauthorized"}}`)
	}))
	t.Cleanup(srv.Close)
	c, err := api.New(srv.URL, "")
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	if _, err := verbRows(t.Context(), c, "sessions"); err == nil ||
		!strings.Contains(err.Error(), "missing credential") {
		t.Fatalf("the gateway's own error must survive, got %v", err)
	}
	if _, err := verbRows(t.Context(), c, "frobnicate"); err == nil {
		t.Fatal("a verb with no fetcher must not report success")
	}
}

// An empty listing and a broken query look identical unless the empty one says
// so in words.
func TestVerbRowsNameAnEmptyListing(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/admin/sessions", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":[]}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c, err := api.New(srv.URL, "k")
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	rows, err := verbRows(t.Context(), c, "sessions")
	if err != nil {
		t.Fatalf("an empty listing is not an error: %v", err)
	}
	if len(rows) != 1 || !strings.Contains(rows[0].Text, "no active dashboard sessions") {
		t.Fatalf("an empty listing must say so, got %#v", rows)
	}
}

// The runCmdMsg arm is the composer's only route into the router.
func TestRunCmdMsgReachesTheRouter(t *testing.T) {
	a := composerApp()
	a.update(runCmdMsg{raw: "frobnicate"})
	if !strings.Contains(transcriptText(a), "frobnicate") {
		t.Fatalf("runCmdMsg must be dispatched:\n%s", transcriptText(a))
	}
}
