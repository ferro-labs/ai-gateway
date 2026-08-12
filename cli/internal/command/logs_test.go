package command

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/ferro-labs/ai-gateway/cli/internal/api"
	"github.com/ferro-labs/ai-gateway/cli/internal/fixture"
)

// executeCtx is execute() with a caller-owned context, which is what a tail
// needs: cancelling it is how SIGINT reaches the poll loop.
func executeCtx(ctx context.Context, args ...string) (stdout, stderr string, err error) {
	root := NewRoot()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs(args)
	err = root.ExecuteContext(ctx)
	return out.String(), errb.String(), err
}

func TestLogsListRendersDashForNullMeasurements(t *testing.T) {
	srv := gateway(t, fixture.Default())
	stdout, _, err := run(t, srv, "logs", "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, col := range []string{"TIME", "TRACE", "PROVIDER", "MODEL", "STAGE", "MS", "COST", "TOKENS"} {
		if !strings.Contains(stdout, col) {
			t.Fatalf("missing column %s in %q", col, stdout)
		}
	}
	// The seeded gpt-4o-mini row has null duration_ms, ttft_ms and cost_usd.
	// Rendering those as 0 would claim a free, instantaneous request.
	var row string
	for _, l := range strings.Split(stdout, "\n") {
		if strings.Contains(l, "gpt-4o-mini") {
			row = l
		}
	}
	if row == "" {
		t.Fatalf("expected the seeded gpt-4o-mini row: %q", stdout)
	}
	// TIME TRACE PROVIDER MODEL STAGE MS COST TOKENS — MS and COST are the two
	// nullable ones on this row, and both must be a dash rather than a zero
	// that would claim a free, instantaneous request.
	fields := strings.Fields(row)
	if len(fields) != 8 {
		t.Fatalf("want 8 whitespace-free columns, got %d in %q", len(fields), row)
	}
	if fields[5] != "-" || fields[6] != "-" {
		t.Fatalf("null duration_ms/cost_usd must render as -, got ms=%q cost=%q", fields[5], fields[6])
	}
	if fields[7] != "856" {
		t.Fatalf("a measured token count must survive: %q", row)
	}
}

func TestLogsListSendsSinceAndFilters(t *testing.T) {
	var got atomic.Pointer[url.Values]
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		got.Store(&q)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[],"summary":{"total_entries":0,"returned_entries":0}}`))
	}))
	defer srv.Close()

	if _, _, err := execute(t, "logs", "list", "--since", "15m", "--provider", "anthropic",
		"--stage", "all", "--limit", "7", "--gateway-url", srv.URL); err != nil {
		t.Fatal(err)
	}
	q := got.Load()
	if q == nil {
		t.Fatal("no request reached the gateway")
	}
	since := q.Get("since")
	if since == "" {
		t.Fatalf("--since must reach the wire: %v", *q)
	}
	ts, err := time.Parse(time.RFC3339, since)
	if err != nil {
		t.Fatalf("since must be RFC3339, got %q", since)
	}
	if d := time.Since(ts); d < 14*time.Minute || d > 16*time.Minute {
		t.Fatalf("15m must mean 15 minutes ago, got %s", d)
	}
	if q.Get("provider") != "anthropic" || q.Get("stage") != "all" || q.Get("limit") != "7" {
		t.Fatalf("filters lost on the way to the wire: %v", *q)
	}
}

func TestLogsListRejectsBadSince(t *testing.T) {
	_, _, err := execute(t, "logs", "list", "--since", "yesterday", "--gateway-url", "http://127.0.0.1:1")
	if err == nil || !strings.Contains(err.Error(), "since") {
		t.Fatalf("an unparseable --since must be reported before any request: %v", err)
	}
}

func TestLogsStatsRendersPercentiles(t *testing.T) {
	srv := gateway(t, fixture.Default())
	stdout, _, err := run(t, srv, "logs", "stats")
	if err != nil {
		t.Fatal(err)
	}
	// Read the rendered rows rather than matching literal totals. The fixture
	// aggregates its own seeded rows now, so a hardcoded "1240" pinned this
	// test to that data and failed the moment the fixture started answering
	// honestly — which is a change to the fixture, not to this renderer.
	got := map[string]string{}
	for _, line := range strings.Split(stdout, "\n") {
		if f := strings.Fields(line); len(f) == 2 {
			got[f[0]] = f[1]
		}
	}
	for _, want := range []string{
		"requests", "error_rate", "cost_usd",
		"latency_p50_ms", "latency_p95_ms", "latency_p99_ms", "ttft_p50_ms",
	} {
		if got[want] == "" {
			t.Fatalf("stats must render %q:\n%s", want, stdout)
		}
	}
	// Percentiles are non-decreasing by construction, so this holds for any
	// data — and it fails if the renderer ever transposes or mislabels the
	// columns, which matching one value in isolation cannot detect.
	p50, p95, p99 := statNum(t, got, "latency_p50_ms"), statNum(t, got, "latency_p95_ms"), statNum(t, got, "latency_p99_ms")
	if p50 > p95 || p95 > p99 {
		t.Fatalf("latency percentiles must be non-decreasing, got %v/%v/%v:\n%s", p50, p95, p99, stdout)
	}
}

func statNum(t *testing.T, rows map[string]string, name string) float64 {
	t.Helper()
	v, err := strconv.ParseFloat(strings.TrimSuffix(rows[name], "%"), 64)
	if err != nil {
		t.Fatalf("%s must render as a number, got %q", name, rows[name])
	}
	return v
}

func TestLogsStatsExposesOnlySupportedFilters(t *testing.T) {
	srv := gateway(t, fixture.Default())
	if _, _, err := run(t, srv, "logs", "stats", "--stage", "on_error"); err != nil {
		t.Fatalf("stage is supported by the stats endpoint: %v", err)
	}
	if _, _, err := run(t, srv, "logs", "stats", "--api-key-id", "key_01"); err == nil {
		t.Fatal("stats must not advertise the list-only api-key-id filter")
	}
}

func TestFormatLogLineKeepsErrorsOnOneLine(t *testing.T) {
	line := formatLogLine(api.LogEntry{Stage: "on_error", ErrorMessage: "first\nsecond\tthird"})
	if strings.ContainsAny(line, "\n\r\t") || !strings.HasSuffix(line, "first second third") {
		t.Fatalf("tail rows must remain one line: %q", line)
	}
}

// A Provider, Model or Stage carrying a space or newline must not widen into
// an extra column or break the one-physical-line contract: every fixed-width
// field is normalized, not only ErrorMessage.
func TestFormatLogLineNormalizesEveryField(t *testing.T) {
	line := formatLogLine(api.LogEntry{
		TraceID:  "trace 1",
		Provider: "open\nai rogue",
		Model:    "gpt 4\to",
		Stage:    "custom\nstage",
	})
	if strings.ContainsAny(line, "\n\r\t") {
		t.Fatalf("a field carrying whitespace must not break the one-line contract: %q", line)
	}
	fields := strings.Fields(line)
	if len(fields) != 8 {
		t.Fatalf("want 8 whitespace-delimited columns even with dirty fields, got %d in %q", len(fields), line)
	}
	if fields[1] != "trace_1" {
		t.Fatalf("trace id's embedded space must not split into a second column: %q", line)
	}
	if fields[2] != "open_ai_rogue" || fields[3] != "gpt_4_o" || fields[4] != "custom_stage" {
		t.Fatalf("provider/model/stage must stay one column each: %q", line)
	}
}

// A gateway with no log store answers 501. That is a feature that is absent,
// not a crash: the message names the missing store instead of the status code.
func TestLogsDegradeWithAHintWhenNoStore(t *testing.T) {
	s := fixture.Default()
	s.NoLogStore = true
	srv := gateway(t, s)

	for _, verb := range []string{"list", "stats", "tail"} {
		_, _, err := run(t, srv, "logs", verb)
		if err == nil {
			t.Fatalf("logs %s must fail when the store is absent", verb)
		}
		if !strings.Contains(err.Error(), "request-log store") {
			t.Fatalf("logs %s must name the missing store, got %v", verb, err)
		}
	}
}

func TestLogsTailPrintsRowsAndExitsCleanlyOnCancel(t *testing.T) {
	srv := gateway(t, fixture.Default())
	t.Setenv("FERRO_API_KEY", "fgw_test")
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()

	stdout, _, err := executeCtx(ctx, "logs", "tail", "--since", "1h",
		"--gateway-url", srv.URL)
	if err != nil {
		t.Fatalf("a tail the operator interrupted is not a failure: %v", err)
	}
	if !strings.Contains(stdout, "anthropic") {
		t.Fatalf("tail must print each new row as a line: %q", stdout)
	}
	if strings.Contains(stdout, "TRACE") {
		t.Fatalf("tail emits lines, not a re-drawn table header: %q", stdout)
	}
}

func TestLogsTailBacksOffAndKeepsGoingOnFailure(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"boom","type":"server_error","code":"server_error"}}`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	stdout, stderr, err := executeCtx(ctx, "logs", "tail",
		"--gateway-url", srv.URL)
	if err != nil {
		t.Fatalf("a poll failure is a warning, not an exit: %v", err)
	}
	if calls.Load() == 0 {
		t.Fatal("the tail never polled")
	}
	if !strings.Contains(stderr, "retrying") {
		t.Fatalf("the failure and the retry delay belong on stderr: %q", stderr)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("a failed poll must not write to the machine channel: %q", stdout)
	}
}

// A poll that hits the follower's page bound keeps the rows it fetched and
// moves its cursor past them, so it is progress: the rows belong on stdout, the
// gap belongs on stderr, and the cadence must not back off as it does for a
// poll that failed and fetched nothing.
func TestLogsTailReportsTruncationWithoutBackingOff(t *testing.T) {
	// The follower asks for 100 rows a page and stops paging early on a short
	// one, so a full page is what keeps it going. The rows carry only an id:
	// this is about the page bound, not about rendering.
	rows := make([]string, 0, 100)
	for i := range 100 {
		rows = append(rows, fmt.Sprintf(`{"trace_id":"trace-%03d"}`, i))
	}
	// A full page every time, against a total the follower can never reach:
	// every poll exhausts its page bound with rows still outstanding.
	body := []byte(`{"data":[` + strings.Join(rows, ",") +
		`],"summary":{"total_entries":1000000,"returned_entries":100}}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	// Long enough for one whole poll — 32 pages of it — then the interrupt.
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()

	stdout, stderr, err := executeCtx(ctx, "logs", "tail", "--gateway-url", srv.URL)
	if err != nil {
		t.Fatalf("a truncated poll is not a failed command: %v", err)
	}
	if !strings.Contains(stdout, "trace-000") {
		t.Fatalf("the rows a truncated poll did fetch must still reach stdout: %q (stderr %q)", stdout, stderr)
	}
	if !strings.Contains(stderr, "skipped") {
		t.Fatalf("the skipped older rows must be stated on stderr: %q", stderr)
	}
	if strings.Contains(stderr, "retrying") {
		t.Fatalf("a truncated poll made progress and must not back off: %q", stderr)
	}
}

func TestBackoffDoublesToCap(t *testing.T) {
	d := tailInterval
	for range 10 {
		d = backoff(d)
	}
	if d != tailMaxInterval {
		t.Fatalf("backoff must double to a %s cap, got %s", tailMaxInterval, d)
	}
}

// The flag set is shared by list and tail, so it is worth one direct test.
func TestLogsQueryFromFlagsPassesNoneSentinel(t *testing.T) {
	cmd := &cobra.Command{}
	addLogFilterFlags(cmd)
	if err := cmd.ParseFlags([]string{"--api-key-id", "none"}); err != nil {
		t.Fatal(err)
	}
	q, err := logsQueryFromFlags(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if q.APIKeyID != "none" {
		t.Fatalf("the gateway's none sentinel must pass through untouched, got %q", q.APIKeyID)
	}
}
