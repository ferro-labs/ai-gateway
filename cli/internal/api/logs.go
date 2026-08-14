package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"
)

// LogEntry is one row of GET /admin/logs. The three measurements are pointers
// because the gateway sends null for "not measured" — a request that failed
// before it reached a provider, or one served by a provider with no price in
// the catalog. Collapsing null into 0 would render as a real zero-cost,
// zero-latency request.
type LogEntry struct {
	TraceID          string    `json:"trace_id"`
	Stage            string    `json:"stage"`
	Model            string    `json:"model"`
	APIKeyID         string    `json:"api_key_id"`
	Provider         string    `json:"provider"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	TotalTokens      int       `json:"total_tokens"`
	ErrorMessage     string    `json:"error_message"`
	CreatedAt        time.Time `json:"created_at"`
	DurationMs       *float64  `json:"duration_ms"`
	TTFTMs           *float64  `json:"ttft_ms"`
	CostUSD          *float64  `json:"cost_usd"`
}

// LogsQuery filters GET /admin/logs. Only non-zero fields are sent: an empty
// value is not the same as an absent one on every parameter, and sending one
// can narrow the listing server-side.
//
// APIKeyID accepts the gateway's "none" sentinel (rows with no credential),
// which is passed through untouched.
type LogsQuery struct {
	Limit    int
	Offset   int
	Since    time.Time
	Model    string
	Provider string
	Stage    string
	APIKeyID string
}

func (q LogsQuery) values() url.Values {
	v := url.Values{}
	if q.Limit > 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	if q.Offset > 0 {
		v.Set("offset", strconv.Itoa(q.Offset))
	}
	if !q.Since.IsZero() {
		v.Set("since", q.Since.UTC().Format(time.RFC3339))
	}
	for k, s := range map[string]string{
		"model": q.Model, "provider": q.Provider, "stage": q.Stage, "api_key_id": q.APIKeyID,
	} {
		if s != "" {
			v.Set(k, s)
		}
	}
	return v
}

// LogsPage is one page of GET /admin/logs.
type LogsPage struct {
	Data    []LogEntry `json:"data"`
	Summary struct {
		TotalEntries    int `json:"total_entries"`
		ReturnedEntries int `json:"returned_entries"`
	} `json:"summary"`
}

// Percentiles is one latency distribution. The gateway sends null for the
// whole block when nothing was measured, so callers hold it by pointer.
type Percentiles struct {
	P50   float64 `json:"p50"`
	P95   float64 `json:"p95"`
	P99   float64 `json:"p99"`
	Max   float64 `json:"max"`
	Mean  float64 `json:"mean"`
	Count int     `json:"count"`
}

// LogStats is GET /admin/logs/stats. The by_* breakdowns stay raw: their value
// shape is not part of any contract ferro renders, and decoding them would
// invent one.
type LogStats struct {
	Summary struct {
		TotalEntries     int     `json:"total_entries"`
		ErrorEntries     int     `json:"error_entries"`
		TotalTokens      int     `json:"total_tokens"`
		PromptTokens     int     `json:"prompt_tokens"`
		CompletionTokens int     `json:"completion_tokens"`
		CostUSD          float64 `json:"cost_usd"`
		UnpricedRequests int     `json:"unpriced_requests"`
	} `json:"summary"`
	LatencyMs  *Percentiles               `json:"latency_ms"`
	TTFTMs     *Percentiles               `json:"ttft_ms"`
	ByProvider map[string]json.RawMessage `json:"by_provider"`
	ByModel    map[string]json.RawMessage `json:"by_model"`
	TopErrors  []struct {
		Message string `json:"message"`
		Count   int    `json:"count"`
	} `json:"top_errors"`
}

// Logs reads a page of the request log. A gateway with no log store configured
// answers 501, for which IsNotSupported reports true.
func (c *Client) Logs(ctx context.Context, q LogsQuery) (*LogsPage, error) {
	var out LogsPage
	if _, err := c.do(ctx, http.MethodGet, "/admin/logs", q.values(), nil, &out, doOpts{}); err != nil {
		return nil, err
	}
	return &out, nil
}

// LogStats reads the pre-aggregated request-log statistics: totals, error
// counts and latency percentiles the CLI would otherwise have to compute from
// a full page walk. The stats endpoint supports time, model, provider, and
// stage filters; paging and credential filters are intentionally omitted.
func (c *Client) LogStats(ctx context.Context, filter LogsQuery) (*LogStats, error) {
	var out LogStats
	q := LogsQuery{Since: filter.Since, Model: filter.Model, Provider: filter.Provider, Stage: filter.Stage}.values()
	if _, err := c.do(ctx, http.MethodGet, "/admin/logs/stats", q, nil, &out, doOpts{}); err != nil {
		return nil, err
	}
	return &out, nil
}

// ParseSince reads a --since value: a Go duration ("15m", "2h") meaning that
// long ago, or an absolute RFC3339 timestamp. A signed duration is read as the
// same span into the past — a future cursor is never what was meant.
func ParseSince(s string) (time.Time, error) {
	if d, err := time.ParseDuration(s); err == nil {
		if d < 0 {
			d = -d
		}
		return time.Now().Add(-d), nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid since %q: want a duration (15m, 2h) or an RFC3339 timestamp", s)
	}
	return t, nil
}

// followDedupeWindow bounds the ring of already-emitted row keys.
//
// The window is row-count based rather than time based. It covers the maximum
// bounded page drain so a boundary row cannot be evicted during one poll.
const (
	maxFollowPages     = 32
	maxFollowLimit     = 200
	followDedupeWindow = maxFollowPages * maxFollowLimit
)

// Follower turns the paged /admin/logs listing into a tail: each Poll advances
// a since-cursor and returns only rows not seen before.
//
// It is not safe for concurrent use — one Follower belongs to one tail loop.
type Follower struct {
	c      *Client
	q      LogsQuery
	cursor time.Time
	seen   map[string]struct{}
	order  []string // FIFO eviction for the dedupe ring
}

// NewFollower starts a tail at q.Since (zero means the gateway's own default
// window).
func NewFollower(c *Client, q LogsQuery) *Follower {
	if q.Limit <= 0 {
		q.Limit = 100
	} else if q.Limit > maxFollowLimit {
		q.Limit = maxFollowLimit
	}
	return &Follower{c: c, q: q, cursor: q.Since, seen: make(map[string]struct{})}
}

// dedupeKey identifies a row. A trace has one row per pipeline stage, so the
// stage is part of the identity; the timestamp separates a retried trace id.
func dedupeKey(e LogEntry) string {
	return e.TraceID + "/" + e.Stage + "/" + e.CreatedAt.UTC().Format(time.RFC3339Nano)
}

// ErrFollowTruncated reports that one poll drained maxFollowPages without
// reaching the end of its window. The rows returned alongside it are complete
// and the cursor has advanced past them, so the next poll resumes instead of
// re-draining the same window: a sustained burst slows the tail down, it never
// stops it. What is lost is the older end of that one window — the gateway
// lists newest first, so the pages the bound cut off are the oldest ones.
var ErrFollowTruncated = fmt.Errorf(
	"request-log poll exceeded the %d-page safety bound; older rows in this window were skipped — narrow the filters or use a shorter interval",
	maxFollowPages)

// Poll fetches rows since the cursor and returns the unseen ones in ascending
// created_at order.
//
// On a fetch error the cursor is left untouched, so a transient failure
// re-fetches the same window rather than skipping the rows that landed during
// it. The cursor is also second-granular while created_at is not, so the
// boundary row is re-fetched by design and dropped by the dedupe ring — the
// safe direction.
//
// ErrFollowTruncated is the one error returned with rows: they are valid and
// the cursor moved, so a caller that renders them loses nothing.
func (f *Follower) Poll(ctx context.Context) ([]LogEntry, error) {
	q := f.q
	q.Since = f.cursor
	rows := make([]LogEntry, 0, q.Limit)
	baseOffset := q.Offset
	drained := false
	for pageNo := 0; pageNo < maxFollowPages; pageNo++ {
		q.Offset = baseOffset + pageNo*q.Limit
		page, err := f.c.Logs(ctx, q)
		if err != nil {
			return nil, err
		}
		rows = append(rows, page.Data...)
		if len(page.Data) < q.Limit || q.Offset+len(page.Data) >= page.Summary.TotalEntries {
			drained = true
			break
		}
	}
	slices.SortStableFunc(rows, func(a, b LogEntry) int { return a.CreatedAt.Compare(b.CreatedAt) })

	fresh := rows[:0]
	for _, e := range rows {
		k := dedupeKey(e)
		if _, dup := f.seen[k]; dup {
			continue
		}
		f.seen[k] = struct{}{}
		f.order = append(f.order, k)
		if len(f.order) > followDedupeWindow {
			delete(f.seen, f.order[0])
			f.order = f.order[1:]
		}
		fresh = append(fresh, e)
		if e.CreatedAt.After(f.cursor) {
			f.cursor = e.CreatedAt
		}
	}
	if !drained {
		return fresh, ErrFollowTruncated
	}
	return fresh, nil
}
