package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLogsDecodesNullMeasurements(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, 200, `{"data":[
			{"trace_id":"t1","stage":"after_request","model":"gpt-5.1","api_key_id":"k1","provider":"openai",
			 "prompt_tokens":10,"completion_tokens":20,"total_tokens":30,"error_message":"",
			 "created_at":"2026-08-08T10:00:01Z","duration_ms":812.5,"ttft_ms":210.25,"cost_usd":0.0042},
			{"trace_id":"t2","stage":"on_error","model":"gpt-5.1","api_key_id":"","provider":"",
			 "prompt_tokens":0,"completion_tokens":0,"total_tokens":0,"error_message":"upstream unavailable",
			 "created_at":"2026-08-08T10:00:02Z","duration_ms":null,"ttft_ms":null,"cost_usd":null}],
			"summary":{"total_entries":2,"returned_entries":2}}`)
	}))
	defer srv.Close()

	page, err := testClient(t, srv.URL, "fgw_ok").Logs(context.Background(), LogsQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if page.Summary.TotalEntries != 2 || page.Summary.ReturnedEntries != 2 || len(page.Data) != 2 {
		t.Fatalf("bad page: %+v", page)
	}
	a := page.Data[0]
	if a.DurationMs == nil || *a.DurationMs != 812.5 || a.TTFTMs == nil || a.CostUSD == nil || *a.CostUSD != 0.0042 {
		t.Fatalf("measured row lost its values: %+v", a)
	}
	if a.PromptTokens != 10 || a.TotalTokens != 30 || a.Provider != "openai" {
		t.Fatalf("bad decode: %+v", a)
	}
	b := page.Data[1]
	// null is "not measured" — it must NOT collapse into 0, which would render
	// as a real zero-cost, zero-latency request.
	if b.DurationMs != nil || b.TTFTMs != nil || b.CostUSD != nil {
		t.Fatalf("null measurements must decode to nil: %+v", b)
	}
	if b.ErrorMessage != "upstream unavailable" {
		t.Fatalf("bad decode: %+v", b)
	}
}

func TestLogsQueryEncoding(t *testing.T) {
	var got url.Values
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, path = r.URL.Query(), r.URL.Path
		writeJSON(t, w, 200, `{"data":[],"summary":{"total_entries":0,"returned_entries":0}}`)
	}))
	defer srv.Close()
	c := testClient(t, srv.URL, "fgw_ok")

	since := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	if _, err := c.Logs(context.Background(), LogsQuery{
		Limit: 50, Offset: 100, Since: since,
		Model: "gpt-5.1", Provider: "openai", Stage: "all", APIKeyID: "none",
	}); err != nil {
		t.Fatal(err)
	}
	if path != "/admin/logs" {
		t.Fatalf("want GET /admin/logs, got %q", path)
	}
	want := url.Values{
		"limit": {"50"}, "offset": {"100"}, "since": {"2026-08-08T10:00:00Z"},
		"model": {"gpt-5.1"}, "provider": {"openai"}, "stage": {"all"},
		// "none" is the gateway's sentinel for credential-less rows: passed
		// through untouched, never interpreted.
		"api_key_id": {"none"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("query = %v, want %v", got, want)
	}

	// A zero query sends no filters at all — an empty value would narrow the
	// listing server-side on some parameters.
	if _, err := c.Logs(context.Background(), LogsQuery{}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("zero query must send no parameters, got %v", got)
	}
}

func TestLogs501IsNotSupported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusNotImplemented,
			`{"error":{"message":"request log store not configured","type":"server_error"}}`)
	}))
	defer srv.Close()

	_, err := testClient(t, srv.URL, "fgw_ok").Logs(context.Background(), LogsQuery{})
	if err == nil || !IsNotSupported(err) {
		t.Fatalf("a 501 must be detectable as unsupported, got %v", err)
	}
}

func TestLogStatsDecodes(t *testing.T) {
	var got url.Values
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, path = r.URL.Query(), r.URL.Path
		writeJSON(t, w, 200, `{
			"summary":{"total_entries":1200,"error_entries":13,"total_tokens":90000,"prompt_tokens":60000,
				"completion_tokens":30000,"cost_usd":1.2345,"unpriced_requests":4},
			"latency_ms":{"p50":210,"p95":980.5,"p99":1400,"max":2200,"mean":320.25,"count":1200},
			"ttft_ms":null,
			"by_stage":{"after_request":{"count":1187}},
			"by_provider":{"openai":{"count":900}},
			"by_model":{"gpt-5.1":{"count":900}},
			"top_errors":[{"message":"upstream unavailable","count":9}],
			"series":{}}`)
	}))
	defer srv.Close()

	since := time.Date(2026, 8, 8, 9, 55, 0, 0, time.UTC)
	st, err := testClient(t, srv.URL, "fgw_ok").LogStats(context.Background(), LogsQuery{
		Since: since, Model: "gpt-5.1", Provider: "openai", Stage: "on_error", APIKeyID: "key_ignored",
	})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/admin/logs/stats" {
		t.Fatalf("want GET /admin/logs/stats, got %q", path)
	}
	if got.Get("since") != "2026-08-08T09:55:00Z" || got.Get("model") != "gpt-5.1" ||
		got.Get("provider") != "openai" || got.Get("stage") != "on_error" || got.Has("api_key_id") {
		t.Fatalf("bad query: %v", got)
	}
	if st.Summary.TotalEntries != 1200 || st.Summary.ErrorEntries != 13 || st.Summary.CostUSD != 1.2345 ||
		st.Summary.UnpricedRequests != 4 {
		t.Fatalf("bad summary: %+v", st.Summary)
	}
	if st.LatencyMs == nil || st.LatencyMs.P95 != 980.5 || st.LatencyMs.Count != 1200 {
		t.Fatalf("latency percentiles: %+v", st.LatencyMs)
	}
	// A null percentile block means "nothing measured" and must stay nil.
	if st.TTFTMs != nil {
		t.Fatalf("null ttft_ms must decode to nil, got %+v", st.TTFTMs)
	}
	if len(st.TopErrors) != 1 || st.TopErrors[0].Count != 9 {
		t.Fatalf("top errors: %+v", st.TopErrors)
	}
	if string(st.ByProvider["openai"]) != `{"count":900}` || len(st.ByModel) != 1 {
		t.Fatalf("breakdowns stay raw: %v", st.ByProvider)
	}
}

func TestParseSince(t *testing.T) {
	now := time.Now()
	got, err := ParseSince("15m")
	if err != nil {
		t.Fatal(err)
	}
	if d := now.Add(-15 * time.Minute).Sub(got); d > time.Second || d < -time.Second {
		t.Fatalf("15m must mean 15 minutes ago, got %v (off by %v)", got, d)
	}
	// A signed duration means the same thing — nobody wants a future cursor.
	// Bound the difference on both sides: an upper bound alone passes a
	// timestamp two hours in the future, which is the bug being guarded.
	got, err = ParseSince("-2h")
	if err != nil {
		t.Fatalf("-2h must parse: %v", err)
	}
	if d := now.Add(-2 * time.Hour).Sub(got); d > time.Second || d < -time.Second {
		t.Fatalf("-2h must also mean two hours ago, got %v (off by %v)", got, d)
	}
	want := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	if got, err = ParseSince("2026-08-08T10:00:00Z"); err != nil || !got.Equal(want) {
		t.Fatalf("RFC3339 must parse: %v %v", got, err)
	}
	if _, err = ParseSince("yesterday"); err == nil {
		t.Fatal("an unparseable value must be an error, not a silent zero time")
	}
	if _, err = ParseSince(""); err == nil {
		t.Fatal("empty must be an error")
	}
}

// followStub serves pages keyed by the `since` parameter and records every
// since value it was asked for.
type followStub struct {
	mu       sync.Mutex
	since    []string
	pages    []string // body per request index
	statuses []int    // 0 means 200
}

func (s *followStub) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		i := len(s.since)
		s.since = append(s.since, r.URL.Query().Get("since"))
		body, status := "", 0
		if i < len(s.pages) {
			body = s.pages[i]
		}
		if i < len(s.statuses) {
			status = s.statuses[i]
		}
		s.mu.Unlock()
		if status != 0 && status != 200 {
			writeJSON(t, w, status, `{"error":{"message":"boom","type":"server_error"}}`)
			return
		}
		writeJSON(t, w, 200, body)
	}
}

func (s *followStub) sinceAt(i int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i >= len(s.since) {
		return ""
	}
	return s.since[i]
}

func logRow(trace, stage, created string) string {
	return fmt.Sprintf(`{"trace_id":%q,"stage":%q,"model":"m","api_key_id":"","provider":"openai",
		"prompt_tokens":0,"completion_tokens":0,"total_tokens":0,"error_message":"",
		"created_at":%q,"duration_ms":null,"ttft_ms":null,"cost_usd":null}`, trace, stage, created)
}

func logPage(rows ...string) string {
	return fmt.Sprintf(`{"data":[%s],"summary":{"total_entries":%d,"returned_entries":%d}}`,
		strings.Join(rows, ","), len(rows), len(rows))
}

func TestFollowerCursorAndDedupe(t *testing.T) {
	a, b, cc := "2026-08-08T10:00:01Z", "2026-08-08T10:00:02Z", "2026-08-08T10:00:03Z"
	stub := &followStub{pages: []string{
		// The gateway serves newest first; the follower must not.
		logPage(logRow("B", "after_request", b), logRow("A", "after_request", a)),
		// since >= B: the boundary row comes back, plus the new one.
		logPage(logRow("C", "after_request", cc), logRow("B", "after_request", b)),
	}}
	srv := httptest.NewServer(stub.handler(t))
	defer srv.Close()

	f := NewFollower(testClient(t, srv.URL, "fgw_ok"), LogsQuery{})
	rows, err := f.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].TraceID != "A" || rows[1].TraceID != "B" {
		t.Fatalf("first poll must be ascending [A B], got %v", traceIDs(rows))
	}
	if s := stub.sinceAt(0); s != "" {
		t.Fatalf("first poll has no cursor yet, sent since=%q", s)
	}

	rows, err = f.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].TraceID != "C" {
		t.Fatalf("B must be deduped, want [C], got %v", traceIDs(rows))
	}
	if s := stub.sinceAt(1); s != b {
		t.Fatalf("cursor must advance to the newest row, sent since=%q want %q", s, b)
	}
}

func TestFollowerDrainsEveryPageBeforeAdvancingCursor(t *testing.T) {
	created := []string{
		"2026-08-08T10:00:04Z", "2026-08-08T10:00:03Z",
		"2026-08-08T10:00:02Z", "2026-08-08T10:00:01Z",
	}
	var mu sync.Mutex
	var offsets []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		mu.Lock()
		offsets = append(offsets, offset)
		mu.Unlock()
		end := min(offset+limit, len(created))
		rows := make([]string, 0, end-offset)
		for i := offset; i < end; i++ {
			rows = append(rows, logRow(fmt.Sprintf("T%d", i), "after_request", created[i]))
		}
		writeJSON(t, w, http.StatusOK, fmt.Sprintf(
			`{"data":[%s],"summary":{"total_entries":%d,"returned_entries":%d}}`,
			strings.Join(rows, ","), len(created), len(rows)))
	}))
	defer srv.Close()

	got, err := NewFollower(testClient(t, srv.URL, "fgw_ok"), LogsQuery{Limit: 2}).Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("all pages must be returned oldest first: %v", traceIDs(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].CreatedAt.Before(got[i-1].CreatedAt) {
			t.Fatalf("all pages must be returned oldest first: %v", traceIDs(got))
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if fmt.Sprint(offsets) != "[0 2]" {
		t.Fatalf("page offsets = %v, want [0 2]", offsets)
	}
}

func TestFollowerTruncatedPollKeepsRowsAndAdvances(t *testing.T) {
	base := time.Date(2026, 8, 8, 10, 10, 0, 0, time.UTC)
	var mu sync.Mutex
	var sinces []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		sinces = append(sinces, r.URL.Query().Get("since"))
		mu.Unlock()
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		// Newest first, and a total the drain can never reach: the sustained
		// burst that used to stall the tail forever.
		row := logRow(fmt.Sprintf("T%d", offset), "after_request",
			base.Add(-time.Duration(offset)*time.Second).Format(time.RFC3339))
		writeJSON(t, w, http.StatusOK,
			`{"data":[`+row+`],"summary":{"total_entries":100000,"returned_entries":1}}`)
	}))
	defer srv.Close()

	f := NewFollower(testClient(t, srv.URL, "fgw_ok"), LogsQuery{Limit: 1})
	rows, err := f.Poll(context.Background())
	if !errors.Is(err, ErrFollowTruncated) {
		t.Fatalf("a poll that hits the page bound must say so: %v", err)
	}
	if len(rows) != maxFollowPages {
		t.Fatalf("rows already fetched must be kept, got %d want %d", len(rows), maxFollowPages)
	}
	if _, err := f.Poll(context.Background()); !errors.Is(err, ErrFollowTruncated) {
		t.Fatalf("the second poll is still truncated: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	// The advancing cursor is the whole fix: discarding it made every poll
	// re-drain the same window, so the operator never saw a row.
	if first, second := sinces[0], sinces[maxFollowPages]; first != "" || second != base.Format(time.RFC3339) {
		t.Fatalf("cursor must advance past the rows returned: poll1 since=%q poll2 since=%q", first, second)
	}
}

func TestFollowerErrorKeepsCursor(t *testing.T) {
	a, b, cc := "2026-08-08T10:00:01Z", "2026-08-08T10:00:02Z", "2026-08-08T10:00:03Z"
	stub := &followStub{
		pages: []string{
			logPage(logRow("A", "after_request", a), logRow("B", "after_request", b)),
			"",
			logPage(logRow("C", "after_request", cc)),
		},
		statuses: []int{200, http.StatusInternalServerError, 200},
	}
	srv := httptest.NewServer(stub.handler(t))
	defer srv.Close()

	f := NewFollower(testClient(t, srv.URL, "fgw_ok"), LogsQuery{})
	if _, err := f.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Poll(context.Background()); err == nil {
		t.Fatal("a 500 must be reported")
	}
	rows, err := f.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].TraceID != "C" {
		t.Fatalf("want [C] after recovery, got %v", traceIDs(rows))
	}
	// The cursor is what guarantees no row is skipped after a transient
	// failure: it must be unchanged by the failed poll.
	if s2, s3 := stub.sinceAt(1), stub.sinceAt(2); s2 != b || s3 != b {
		t.Fatalf("cursor must survive the failure: poll2 since=%q poll3 since=%q want %q", s2, s3, b)
	}
}

func TestFollowerSameStageDifferentTraceNotDeduped(t *testing.T) {
	ts := "2026-08-08T10:00:01Z"
	stub := &followStub{pages: []string{
		logPage(logRow("A", "after_request", ts), logRow("A", "on_error", ts), logRow("B", "after_request", ts)),
	}}
	srv := httptest.NewServer(stub.handler(t))
	defer srv.Close()

	rows, err := NewFollower(testClient(t, srv.URL, "fgw_ok"), LogsQuery{}).Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Dedupe is trace+stage+time: one trace's two stages are two rows.
	if len(rows) != 3 {
		t.Fatalf("want 3 distinct rows, got %d: %v", len(rows), traceIDs(rows))
	}
}

func TestFollowerDedupeRingStaysBounded(t *testing.T) {
	base := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	initial := make([]string, 0, followDedupeWindow)
	for i := range followDedupeWindow {
		initial = append(initial, logRow(fmt.Sprintf("t%d", i), "after_request",
			base.Add(time.Duration(i)*time.Second).UTC().Format(time.RFC3339)))
	}
	const freshRows = 100
	next := make([]string, 0, freshRows+1)
	next = append(next, initial[len(initial)-1]) // inclusive cursor boundary
	for i := range freshRows {
		next = append(next, logRow(fmt.Sprintf("new%d", i), "after_request",
			base.Add(time.Duration(followDedupeWindow+i)*time.Second).UTC().Format(time.RFC3339)))
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rows := initial
		if r.URL.Query().Get("since") != "" {
			rows = next
		}
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		end := min(offset+limit, len(rows))
		pageRows := rows[offset:end]
		writeJSON(t, w, http.StatusOK, fmt.Sprintf(
			`{"data":[%s],"summary":{"total_entries":%d,"returned_entries":%d}}`,
			strings.Join(pageRows, ","), len(rows), len(pageRows)))
	}))
	defer srv.Close()

	f := NewFollower(testClient(t, srv.URL, "fgw_ok"), LogsQuery{Limit: 1000})
	got, err := f.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != followDedupeWindow {
		t.Fatalf("first poll = %d rows, want %d", len(got), followDedupeWindow)
	}
	got, err = f.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 100 {
		t.Fatalf("boundary row must dedupe while 100 new rows pass, got %d", len(got))
	}
	// A long tail session must not grow a set of every row it ever saw.
	if len(f.seen) > followDedupeWindow || len(f.order) > followDedupeWindow {
		t.Fatalf("dedupe ring unbounded: seen=%d order=%d", len(f.seen), len(f.order))
	}
}

func TestNewFollowerNormalizesNegativeLimit(t *testing.T) {
	f := NewFollower(nil, LogsQuery{Limit: -1})
	if f.q.Limit != 100 {
		t.Fatalf("negative limit must use the safe default, got %d", f.q.Limit)
	}
}

func TestFollowerPropagates501(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusNotImplemented, `{"error":{"message":"no log store","type":"server_error"}}`)
	}))
	defer srv.Close()

	_, err := NewFollower(testClient(t, srv.URL, "fgw_ok"), LogsQuery{}).Poll(context.Background())
	if !IsNotSupported(err) {
		t.Fatalf("the tail must be able to degrade one screen on 501: %v", err)
	}
}

func traceIDs(rows []LogEntry) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.TraceID+"/"+r.Stage)
	}
	return out
}
