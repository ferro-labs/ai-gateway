package tui

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ferro-labs/ai-gateway/cli/internal/api"
	"github.com/ferro-labs/ai-gateway/cli/internal/config"
	"github.com/ferro-labs/ai-gateway/cli/internal/tui/theme"
)

// testApp builds a fully-populated shell with no client: every test here is
// pure MVU — no terminal, no program loop, no network.
func testApp(w, h int) *App {
	a := newApp(nil, config.Resolved{ProfileName: "local", URL: "http://localhost:8080"}, theme.Mode{})
	a.Width, a.Height = w, h
	a.Status = &api.StatusReport{
		State:     "connected",
		URL:       "http://localhost:8080",
		LatencyMs: 23,
		Providers: api.Count(8),
		Models:    api.Count(2500),
		Targets:   &api.TargetSummary{Total: 8, Routable: 7},
		Circuits:  api.CircuitSummary{Open: 0, HalfOpen: 1},
		MCP:       &api.MCPSummary{Ready: 3, Total: 4},
		Auth:      "admin",
	}
	a.StatusAt = time.Now()
	a.Traffic = testStats(47700, 200, 916)
	a.Rail = RailData{
		Providers: []api.AdminProviderHealth{
			{Name: "openai", Status: "healthy", Models: 1104},
			{Name: "anthropic", Status: "healthy", Models: 412},
			{Name: "gemini", Status: "healthy", Models: 96},
			{Name: "groq", Status: "healthy", Models: 40},
			{Name: "mistral", Status: "healthy", Models: 32},
			{Name: "cohere", Status: "healthy", Models: 18},
			{Name: "deepseek", Status: "healthy", Models: 9},
			{Name: "xai", Status: "healthy", Models: 6},
		},
		MCPReady: 3, MCPTotal: 4,
		PluginsActive: 6, Sessions: 2, AuditToday: 4,
	}
	return a
}

func testStats(total, errs int, p95 float64) *api.LogStats {
	s := &api.LogStats{}
	s.Summary.TotalEntries = total
	s.Summary.ErrorEntries = errs
	s.LatencyMs = &api.Percentiles{P50: 210, P95: p95, P99: 1800, Count: total}
	return s
}

func TestBreakpointComposition(t *testing.T) {
	a := testApp(126, 40)
	view := a.render()
	for _, s := range []string{"CONNECTED PROVIDERS", "SERVICES", "TARGETS", "TRAFFIC", "COMMAND"} {
		if !strings.Contains(view, s) {
			t.Fatalf("wide view missing %q:\n%s", s, view)
		}
	}

	a.Width = 96
	if v := a.render(); strings.Contains(v, "CONNECTED PROVIDERS") || !strings.Contains(v, "STATUS") {
		t.Fatalf("medium must collapse rail into STATUS frame:\n%s", v)
	}

	a.Width = 72
	if v := a.render(); strings.Contains(v, "STATUS") || !strings.Contains(v, "COMMAND") {
		t.Fatalf("narrow is pane + composer only:\n%s", v)
	}
}

// TestEveryViewLineFitsWidth is the box-drawing guard: a single line wider than
// the terminal wraps and destroys every frame below it.
func TestEveryViewLineFitsWidth(t *testing.T) {
	for _, mode := range []theme.Mode{{}, {Color: true}, {ASCII: true}, {Color: true, ASCII: true}} {
		for _, w := range []int{72, 96, 126} {
			a := testApp(w, 40)
			a.Theme = theme.New(mode)
			for i, line := range strings.Split(a.render(), "\n") {
				if lw := lipgloss.Width(line); lw > w {
					t.Fatalf("mode=%+v w=%d line %d overflows: %d cells %q", mode, w, i, lw, line)
				}
			}
		}
	}
}

// The same guard, with each screen and a modal on show: the box drawing has to
// survive what the panes put inside it, not just an empty transcript.
func TestEveryViewLineFitsWidthOnEveryScreen(t *testing.T) {
	now := time.Now()
	for _, mode := range []theme.Mode{{}, {Color: true}, {ASCII: true}, {Color: true, ASCII: true}} {
		for _, w := range []int{72, 96, 126} {
			a := composerApp()
			a.Theme, a.Width = theme.New(mode), w

			a.route("logs --since 15m --model claude-sonnet-4-6")
			a.update(logsBatchMsg{gen: a.Logs.gen, rows: []api.LogEntry{
				logRow("tr_00000001", now), logRow("tr_00000002", now.Add(time.Second)),
			}})
			a.Logs.sel, a.Logs.detail = 0, true

			a.update(keysListMsg{gen: a.Keys.gen, keys: []api.Key{
				{ID: "key_01", Key: "fgw_abcd…wxyz", Name: "ops-laptop", Scopes: []string{"admin"}, Active: true},
			}})
			a.Keys.sel, a.Keys.detail = 0, true

			a.route("playground")
			a.Play.submit(a, "a prompt long enough to wrap at every width under test")
			a.update(chatDeltaMsg{gen: a.Play.gen, text: strings.Repeat("streamed words ", 12)})
			a.update(flushTickMsg{gen: a.Play.gen})
			a.update(chatEndMsg{gen: a.Play.gen, done: true})
			a.update(chatMetaMsg{gen: a.Play.gen, row: &api.LogEntry{Provider: "anthropic", CostUSD: f64(0.0031)}})

			for _, screen := range []Screen{ScreenHome, ScreenLogs, ScreenKeys, ScreenPlayground} {
				a.Screen = screen
				for _, modal := range []*Modal{nil, {
					Title:       "Rotate ops-laptop?",
					Body:        "Blast radius: every client holding the current secret fails immediately\nuntil it is redeployed with the new one.",
					Rows:        []ModalKV{{K: "id", V: "key_01"}, {K: "last used", V: "3 minutes ago · 41,000 requests", Warn: true}},
					Input:       true,
					Placeholder: `type "ops-laptop" to confirm`,
					Confirm:     "ops-laptop",
					OKLabel:     "rotate",
					Danger:      true,
				}} {
					a.Modal = modal
					for i, line := range strings.Split(a.render(), "\n") {
						if lw := lipgloss.Width(line); lw > w {
							t.Fatalf("mode=%+v w=%d screen=%v modal=%t line %d overflows: %d cells %q",
								mode, w, screen, modal != nil, i, lw, line)
						}
					}
				}
			}
		}
	}
}

func TestNarrowUnderMinimumStillRenders(t *testing.T) {
	a := testApp(20, 12)
	for i, line := range strings.Split(a.render(), "\n") {
		if lw := lipgloss.Width(line); lw > 20 {
			t.Fatalf("line %d overflows at w=20: %d cells %q", i, lw, line)
		}
	}
}

func TestFailedPollKeepsLastGoodStatus(t *testing.T) {
	a := testApp(126, 40)
	a.Status, a.StatusErr = nil, nil

	good := &api.StatusReport{State: "connected", Providers: api.Count(3)}
	a.update(statusMsg{gen: 0, report: good})
	a.update(statusMsg{gen: 0, err: errors.New("boom")})

	if a.Status != good || a.StatusErr == nil {
		t.Fatalf("stale data must persist with its age; errors annotate, never erase (status=%v err=%v)", a.Status, a.StatusErr)
	}
	if !strings.Contains(a.render(), "stale") {
		t.Fatal("a stale header must say so")
	}
}

func TestStalePollGenerationDropped(t *testing.T) {
	a := testApp(126, 40)
	a.Status = nil
	a.pollGen = 1
	a.update(statusMsg{gen: 0, report: &api.StatusReport{State: "connected"}})
	if a.Status != nil {
		t.Fatal("results from a previous generation must be dropped")
	}
	a.update(trafficMsg{gen: 0, stats: testStats(10, 0, 5)})
	a.update(railMsg{gen: 0, data: RailData{PluginsActive: 99}})
	if a.Traffic == nil || a.Traffic.Summary.TotalEntries == 10 {
		t.Fatal("stale trafficMsg must be dropped too")
	}
	if a.Rail.PluginsActive == 99 {
		t.Fatal("stale railMsg must be dropped too")
	}
}

// There is no gateway version over HTTP. The header must say so, not guess.
func TestHeaderNeverInventsAGatewayVersion(t *testing.T) {
	v := testApp(126, 40).render()
	if !strings.Contains(v, "gateway "+emDash) {
		t.Fatalf("header must render `gateway %s`:\n%s", emDash, v)
	}
	if m := regexp.MustCompile(`gateway v?\d`).FindString(v); m != "" {
		t.Fatalf("header fabricated a gateway version: %q", m)
	}
}

func TestNilTrafficRendersDash(t *testing.T) {
	a := testApp(126, 40)
	a.Traffic = nil
	v := a.render()
	for _, label := range []string{"RPS", "P95", "Errors"} {
		row := findRow(v, label)
		if !strings.Contains(row, emDash) {
			t.Fatalf("unknown %s must render %s, got %q", label, emDash, row)
		}
		for _, bad := range []string{"0.0", "NaN", "0ms", "0.00%"} {
			if strings.Contains(row, bad) {
				t.Fatalf("unknown %s must not render %q: %q", label, bad, row)
			}
		}
	}

	// A measured window with no requests still has no percentiles.
	a.Traffic = &api.LogStats{}
	v = a.render()
	if !strings.Contains(findRow(v, "P95"), emDash) {
		t.Fatalf("nil LatencyMs must render %s: %q", emDash, findRow(v, "P95"))
	}
	if strings.Contains(findRow(v, "Errors"), "NaN") {
		t.Fatalf("0/0 errors must not divide: %q", findRow(v, "Errors"))
	}
}

// A gateway that answers /readyz with a bare 503 reports no targets at all.
// Rendering that as 0 would say "none are configured" during exactly the
// outage the header is read in — and dereferencing it would take the console
// down instead.
func TestAbsentTargetsRenderDashNotZero(t *testing.T) {
	for _, w := range []int{72, 96, 126} {
		a := testApp(w, 40)
		a.Status.State, a.Status.Targets = "unreachable", nil
		v := a.render()
		for _, bad := range []string{"0 targets", "0/0"} {
			if strings.Contains(v, bad) {
				t.Fatalf("w=%d: unreported targets must never render %q:\n%s", w, bad, v)
			}
		}
		if w < mediumMin {
			continue // narrow drops both the counts header and the panels
		}
		if row := findRow(v, "Healthy"); !strings.Contains(row, a.dash()) {
			t.Fatalf("w=%d: unreported targets must render a dash: %q", w, row)
		}
	}

	// The wide header is the one place the total is spelled out in words.
	a := testApp(126, 40)
	a.Status.Targets = nil
	if v := a.render(); !strings.Contains(v, a.dash()+" targets") {
		t.Fatalf("the counts row must say %s targets, not invent one:\n%s", a.dash(), v)
	}
}

func TestCompactStatusDoesNotInventZeroMeasurements(t *testing.T) {
	a := testApp(96, 30)
	a.Status = &api.StatusReport{State: "connected", URL: "http://localhost:8080"}
	view := a.viewCompactStatus()
	gatewayRow := findRow(view, "Gateway")
	providersRow := findRow(view, "Providers")
	if !strings.Contains(gatewayRow, a.dash()) || strings.Contains(gatewayRow, "0ms") ||
		!strings.Contains(providersRow, a.dash()) || strings.Contains(providersRow, "0") {
		t.Fatalf("unreported latency and provider counts must render as dashes:\n%s", view)
	}
}

func TestTrafficDerivedFromLogStats(t *testing.T) {
	v := testApp(126, 40).render()
	for label, want := range map[string]string{
		"RPS":    "159.0", // 47700 entries / 300s
		"P95":    "916ms",
		"Errors": "0.42%", // 200 / 47700
	} {
		if row := findRow(v, label); !strings.Contains(row, want) {
			t.Fatalf("%s row must contain %q, got %q", label, want, row)
		}
	}
}

func TestHeaderRendersRealStatusOnly(t *testing.T) {
	v := testApp(126, 40).render()
	for _, want := range []string{
		"Ferro Labs",
		"AI Gateway",
		"connected",
		"local",
		"http://localhost:8080",
		"23ms",
		"8 targets",
		"2,500 models",
	} {
		if !strings.Contains(v, want) {
			t.Fatalf("header missing %q:\n%s", want, v)
		}
	}
}

func TestUnreachableStateGetsBadGlyph(t *testing.T) {
	a := testApp(126, 40)
	a.Status = &api.StatusReport{State: "unreachable", URL: "http://localhost:8080"}
	g := a.Theme.Mode.Glyphs()
	if v := a.render(); !strings.Contains(v, g.Bad+" unreachable") {
		t.Fatalf("unreachable must carry the bad glyph:\n%s", v)
	}
	a.Status.State = "degraded"
	if v := a.render(); !strings.Contains(v, g.Warn+" degraded") {
		t.Fatalf("degraded must carry the warn glyph:\n%s", v)
	}
}

func TestRailReportsMissingCredential(t *testing.T) {
	a := testApp(126, 40)
	a.Rail = RailData{AuthError: true, PluginsActive: -1, Sessions: -1, AuditToday: -1}
	v := a.render()
	if !strings.Contains(v, "auth required") {
		t.Fatalf("unauthorized rail must say so:\n%s", v)
	}
	if !strings.Contains(findRow(v, "Plugins"), emDash) {
		t.Fatalf("unknown service counts render %s: %q", emDash, findRow(v, "Plugins"))
	}
}

func TestWindowSizeAndQuitKeys(t *testing.T) {
	a := testApp(80, 24)
	a.update(tea.WindowSizeMsg{Width: 126, Height: 40})
	if a.Width != 126 || a.Height != 40 {
		t.Fatalf("resize ignored: %dx%d", a.Width, a.Height)
	}
	if cmd := a.update(tea.KeyPressMsg{Mod: tea.ModCtrl, Code: 'c'}); cmd == nil {
		t.Fatal("ctrl+c must return a quit command")
	}
	a.Screen = ScreenLogs
	a.update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if a.Screen != ScreenHome {
		t.Fatal("esc must return to home")
	}
}

func TestPollBackoffDoublesAndResets(t *testing.T) {
	a := testApp(126, 40)
	a.update(statusMsg{err: errors.New("boom")})
	a.update(statusMsg{err: errors.New("boom")})
	if a.statusEvery != 4*statusPoll {
		t.Fatalf("two failures must back off to %v, got %v", 4*statusPoll, a.statusEvery)
	}
	for range 10 {
		a.update(statusMsg{err: errors.New("boom")})
	}
	if a.statusEvery != maxPoll {
		t.Fatalf("backoff must cap at %v, got %v", maxPoll, a.statusEvery)
	}
	a.update(statusMsg{report: &api.StatusReport{State: "connected"}})
	if a.statusEvery != statusPoll {
		t.Fatalf("a good poll must reset the cadence, got %v", a.statusEvery)
	}
}

func TestCommaGrouping(t *testing.T) {
	for in, want := range map[int]string{0: "0", 12: "12", 999: "999", 1000: "1,000", 2500: "2,500", 1234567: "1,234,567"} {
		if got := comma(in); got != want {
			t.Fatalf("comma(%d) = %q, want %q", in, got, want)
		}
	}
}

// findRow returns the first rendered line mentioning label, for asserting on
// one panel row without pinning the whole layout.
func findRow(view, label string) string {
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, label) {
			return line
		}
	}
	return ""
}

// railStub answers the four sources fetchRail polls. Any handler left nil
// answers 500, so a test only wires the endpoints its scenario cares about.
func railStub(health, plugins, sessions, audit http.HandlerFunc) *httptest.Server {
	mux := http.NewServeMux()
	fail := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }
	for path, h := range map[string]http.HandlerFunc{
		"/admin/health": health, "/admin/plugins": plugins,
		"/admin/sessions": sessions, "/admin/audit": audit,
	} {
		if h == nil {
			h = fail
		}
		mux.HandleFunc(path, h)
	}
	return httptest.NewServer(mux)
}

// A gateway poll fans out to four independent sources, and one of them can
// fail while the other three answer fine. fetchRail must not let that one
// failure blank the whole rail: a source that errors THIS round keeps prev's
// last good value for it, while a source that succeeds still refreshes.
func TestFetchRailKeepsLastGoodDataWhenOneSourceFails(t *testing.T) {
	srv := railStub(nil, // /admin/health fails — left unwired, answers 500
		func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `[{"name":"rate-limit","type":"ratelimit","enabled":true}]`)
		},
		func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"data":[{"id":"s1"}]}`)
		},
		func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"data":[],"summary":{"total_entries":3,"returned_entries":0}}`)
		})
	t.Cleanup(srv.Close)
	c, err := api.New(srv.URL, "k")
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	prev := RailData{
		Providers: []api.AdminProviderHealth{{Name: "anthropic", Status: "healthy", Models: 412}},
		MCPReady:  2, MCPTotal: 2,
	}
	msg, ok := fetchRail(c, 0, prev)().(railMsg)
	if !ok {
		t.Fatal("fetchRail must answer a railMsg")
	}

	if msg.err == nil {
		t.Fatal("the failing source must still be reported")
	}
	if len(msg.data.Providers) != 1 || msg.data.Providers[0].Name != "anthropic" {
		t.Fatalf("a failed health poll must keep the last known providers, got %+v", msg.data.Providers)
	}
	if msg.data.MCPTotal != 2 || msg.data.MCPReady != 2 {
		t.Fatalf("a failed health poll must keep the last known MCP counts, got %d/%d ready", msg.data.MCPReady, msg.data.MCPTotal)
	}
	if msg.data.PluginsActive != 1 {
		t.Fatalf("a source that succeeded this round must still refresh, got plugins=%d", msg.data.PluginsActive)
	}
	if msg.data.Sessions != 1 {
		t.Fatalf("a source that succeeded this round must still refresh, got sessions=%d", msg.data.Sessions)
	}
	if msg.data.AuditToday != 3 {
		t.Fatalf("a source that succeeded this round must still refresh, got audit=%d", msg.data.AuditToday)
	}
}

// AuthError is a verdict about the round just polled, not a sticky flag: a
// credential fixed after a 401 must clear it, or the rail would keep telling
// the operator to re-authenticate after they already have.
func TestFetchRailClearsAuthErrorOnceTheCredentialWorks(t *testing.T) {
	answer := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) }
	}
	srv := railStub(
		answer(`{"status":"ok","providers":[]}`),
		answer(`[]`),
		answer(`{"data":[]}`),
		answer(`{"data":[],"summary":{"total_entries":0,"returned_entries":0}}`),
	)
	t.Cleanup(srv.Close)
	c, err := api.New(srv.URL, "k")
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	msg, ok := fetchRail(c, 0, RailData{AuthError: true})().(railMsg)
	if !ok {
		t.Fatal("fetchRail must answer a railMsg")
	}
	if msg.data.AuthError {
		t.Fatal("a since-fixed credential must clear AuthError, not carry it forward forever")
	}
}

// Every counter fetchRail derives by walking a list must be recomputed from
// the round it is walking, not added to the round before it. d starts as prev
// so a failed source keeps its last good value — which turns any bare ++ into
// a running total. Feeding a poll's own answer back in as prev is the only
// shape that catches it: one poll in isolation always looks right.
func TestFetchRailCountersDoNotAccumulateAcrossPolls(t *testing.T) {
	srv := railStub(
		func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"status":"healthy","providers":[{"name":"openai","status":"healthy","models":7}],`+
				`"mcp_servers":[{"name":"fs","ready":true},{"name":"search","ready":false}]}`)
		},
		func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `[{"name":"rate-limit","type":"ratelimit","enabled":true}]`)
		},
		func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"data":[{"id":"s1"}]}`)
		},
		func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"data":[],"summary":{"total_entries":3,"returned_entries":0}}`)
		})
	t.Cleanup(srv.Close)
	c, err := api.New(srv.URL, "k")
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	first, ok := fetchRail(c, 0, RailData{})().(railMsg)
	if !ok {
		t.Fatal("fetchRail must answer a railMsg")
	}
	if first.data.MCPTotal != 2 || first.data.MCPReady != 1 {
		t.Fatalf("first poll must count the served list: total=%d ready=%d",
			first.data.MCPTotal, first.data.MCPReady)
	}

	// The gateway has not changed, so neither may the counts.
	second, ok := fetchRail(c, 0, first.data)().(railMsg)
	if !ok {
		t.Fatal("fetchRail must answer a railMsg")
	}
	if second.data.MCPTotal != 2 || second.data.MCPReady != 1 {
		t.Fatalf("an unchanged gateway must report unchanged counts, got total=%d ready=%d",
			second.data.MCPTotal, second.data.MCPReady)
	}
	if second.data.PluginsActive != first.data.PluginsActive {
		t.Fatalf("plugin count drifted across polls: %d then %d",
			first.data.PluginsActive, second.data.PluginsActive)
	}
}
