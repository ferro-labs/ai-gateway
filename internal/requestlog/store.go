// Package requestlog provides persistent storage primitives for request/response logs.
package requestlog

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/migrations"
	"github.com/ferro-labs/ai-gateway/internal/redact"
	"github.com/ferro-labs/ai-gateway/internal/sqldb"
)

const (
	// defaultListLimit is the page size applied when a List query omits Limit.
	defaultListLimit = 50
	// maxListLimit caps the page size a List query may request.
	maxListLimit = 200
	// maxSeriesBuckets caps the resolution of a Stats time series.
	maxSeriesBuckets = 240
	// seriesScanLimit bounds how many rows a time series is built from.
	//
	// The series is bucketed in Go rather than by a SQL GROUP BY on a date
	// expression, because there is no such expression that works on both
	// backends: the SQLite driver stores created_at as Go's own rendering of a
	// time.Time ("2026-07-23 11:01:43.5 +0000 UTC"), which every SQLite date
	// function returns NULL for. Range filtering survives that only because the
	// format happens to sort lexicographically. Bucketing over a bounded scan
	// needs no date function on either backend and no rewrite of stored rows.
	//
	// A range holding more rows than this yields a series covering only the
	// most recent ones, which StatsResult reports rather than passing off as
	// the whole window.
	defaultSeriesScanLimit = 20000
)

// seriesScanLimit is a variable so the truncation path can be exercised without
// a test inserting twenty thousand rows. Never reassigned outside tests.
var seriesScanLimit = defaultSeriesScanLimit

// The plugin stages this store records, named here rather than imported from
// the plugin package so storage does not depend on the pipeline that feeds it.
// The aggregation SQL already spells "on_error" the same way.
const (
	stageAfterRequest = "after_request"
	stageOnError      = "on_error"
)

// TerminalStages are the stages at which a request has reached an outcome.
//
// Exactly one of them is written per request, which is what makes them the
// stage set a caller filters by to get one row per request rather than one row
// per logged event. A `before_request` row is the same request counted a
// second time, with no provider, no tokens and no duration yet.
func TerminalStages() []string {
	return []string{stageAfterRequest, stageOnError}
}

func isTerminalStage(stage string) bool {
	return stage == stageAfterRequest || stage == stageOnError
}

// Entry represents a persistent request log event emitted by logging plugins.
//
// The three measurements are pointers because absent and zero are different
// facts. A request whose model the catalog does not price has an unknown cost,
// not a cost of nothing; a non-streaming request has no time to first token at
// all; and every row written before these columns existed has none of the
// three. Storing zero for any of those would understate a bill or invent a
// latency, silently and permanently.
type Entry struct {
	TraceID string `json:"trace_id" yaml:"trace_id"`
	Stage   string `json:"stage" yaml:"stage"`
	Model   string `json:"model" yaml:"model"`
	// APIKeyID is the opaque identifier of the credential the request was served
	// under — never the credential itself. It is what makes a row answer "who
	// consumed this", which token counts alone cannot: a response served from
	// the shared cache carries the tokens of the request that primed it and was
	// consumed by whoever asked for it second.
	//
	// Empty for a request that carried no credential, which is every request on
	// a gateway running without key auth.
	APIKeyID         string    `json:"api_key_id" yaml:"api_key_id"`
	Provider         string    `json:"provider" yaml:"provider"`
	PromptTokens     int       `json:"prompt_tokens" yaml:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens" yaml:"completion_tokens"`
	TotalTokens      int       `json:"total_tokens" yaml:"total_tokens"`
	ErrorMessage     string    `json:"error_message" yaml:"error_message"`
	CreatedAt        time.Time `json:"created_at" yaml:"created_at"`
	// DurationMs is the end-to-end time the gateway spent on the request,
	// excluding the after-request plugins — one of which is the writer of this
	// row, and a latency figure that includes its own recording is not one.
	DurationMs *float64 `json:"duration_ms" yaml:"duration_ms"`
	// TTFTMs is the time to first token. Streaming requests only: there is no
	// first token to wait for when the whole response arrives at once.
	TTFTMs *float64 `json:"ttft_ms" yaml:"ttft_ms"`
	// CostUSD is the estimated cost from the model catalog. Nil when the
	// catalog does not price the model, which is not the same as free.
	CostUSD *float64 `json:"cost_usd" yaml:"cost_usd"`
}

// Query defines request log listing filters.
type Query struct {
	Limit  int
	Offset int
	Stage  string
	// Stages restricts the query to a set of stages, for the caller that wants
	// one row per request and so asks for the terminal stages rather than a
	// single one. Ignored when empty; ANDed with Stage when both are set,
	// which no caller does.
	Stages   []string
	Model    string
	Provider string
	// APIKeyID, when non-nil, restricts the query to the rows recorded against
	// that credential id. Nil applies no filter.
	//
	// A pointer to "" selects the rows that name no credential. Two different
	// facts land there: an unauthenticated request records an empty value, and a
	// row written before the column existed holds NULL. Storage keeps them
	// apart and this deliberately folds them, because the question asked of the
	// filter is "which traffic cannot be attributed" and both answer yes —
	// while a filter that returned only one of them would leave rows on screen
	// that no filter value selects.
	APIKeyID *string
	Since    *time.Time
	// SeriesBuckets, when positive, asks Stats to also return a time series of
	// that many buckets. Ignored unless Since is set — there is no window to
	// divide without a lower bound. Capped at maxSeriesBuckets.
	SeriesBuckets int
	// TopErrors, when positive, asks Stats to also return that many distinct
	// error messages, most frequent first.
	TopErrors int
}

// MaintenanceQuery defines filters for request log cleanup operations.
type MaintenanceQuery struct {
	Before   *time.Time
	Stage    string
	Model    string
	Provider string
}

// ListResult is a paginated request log query response.
type ListResult struct {
	Data  []Entry
	Total int
}

// DimensionStat is one group's aggregate within a dimension.
//
// The aggregation query has always computed all three of these per group and
// the result kept only Count, so "which model burns the tokens" and "which
// provider produces the errors" were being discarded at the point they were
// already known.
type DimensionStat struct {
	Count  int
	Errors int
	Tokens int
	// CostUSD is the estimated spend attributed to this group. Rows the catalog
	// could not price contribute nothing, so this is a floor on real spend
	// rather than a total — Unpriced reports how many rows were skipped.
	CostUSD  float64
	Unpriced int
}

// SeriesPoint is one time bucket of request activity.
//
// Requests counts events that reached an outcome — after_request plus on_error
// — not every logged row. The logger writes one row per plugin stage, so
// counting rows would report roughly double the traffic. This is the same rule
// the summary counts use, so the chart and the figures above it agree.
type SeriesPoint struct {
	Start            time.Time
	Requests         int
	Errors           int
	PromptTokens     int
	CompletionTokens int
}

// ErrorGroup is a distinct failure message and how often it occurred.
type ErrorGroup struct {
	Message string
	Count   int
}

// StatsResult is an aggregated summary of the request logs matching a Query's filters.
type StatsResult struct {
	// TotalEntries counts requests, not logged rows: a request writes one row
	// per plugin stage, and summing them reported roughly double the traffic.
	TotalEntries     int
	ErrorEntries     int
	TotalTokens      int
	PromptTokens     int
	CompletionTokens int
	ByStage          map[string]DimensionStat
	ByProvider       map[string]DimensionStat
	ByModel          map[string]DimensionStat
	// Series is empty unless Query.SeriesBuckets and Query.Since are both set.
	Series []SeriesPoint
	// SeriesStart and SeriesEnd bound what Series actually covers. When
	// SeriesTruncated is set they describe the scanned rows rather than the
	// requested window, so a caller plots the span it really has instead of
	// rendering the uncovered remainder as a run of zeros — which reads as an
	// outage rather than as missing data.
	SeriesStart     time.Time
	SeriesEnd       time.Time
	SeriesTruncated bool
	// TopErrors is empty unless Query.TopErrors is set.
	TopErrors []ErrorGroup
	// TotalCostUSD is the summed estimate over requests the catalog could price.
	// UnpricedRequests counts the completed requests it could not, so a caller
	// can say "at least $X, and N requests are not priced" rather than
	// presenting a floor as a total.
	TotalCostUSD     float64
	UnpricedRequests int
	// Latency and TTFT percentiles, in milliseconds, over the same bounded scan
	// that produces Series — so they describe the most recent rows when
	// SeriesTruncated is set. Nil when nothing was measured: a gateway that has
	// never recorded a duration has no p50, which is not the same as 0ms.
	LatencyMs *Percentiles
	TTFTMs    *Percentiles
}

// Percentiles summarises a measured distribution. Mean is included because it
// is what a per-request average is read off, and it moves for reasons the
// percentiles do not.
type Percentiles struct {
	P50   float64
	P95   float64
	P99   float64
	Max   float64
	Mean  float64
	Count int
}

// Writer persists request log entries.
type Writer interface {
	Write(ctx context.Context, entry Entry) error
}

// ErrorAnnotator attaches a failure to a terminal row the same request already
// produced, for the caller that would otherwise have to write a second one.
//
// It exists because a request can complete and then fail: the provider answers,
// the after_request row is written, and an after_request plugin ordered behind
// the writer breaks. Recording that with an on_error row would put two terminal
// rows in the trail for one request, which double-counts it in the default
// listing and in every figure Stats derives — exactly when an operator is
// reading them to diagnose the failure.
//
// It is a separate interface rather than a Writer method because annotating is
// meaningless for a sink that keeps no rows to revisit, and a caller that finds
// a Writer without it simply writes as before.
type ErrorAnnotator interface {
	// AnnotateError records message on the row this trace already wrote at
	// stage. It reports whether a row was found: false means there is nothing
	// to annotate and the caller still owns recording the failure.
	AnnotateError(ctx context.Context, traceID, stage, message string) (bool, error)
}

// WriterReceiver is implemented by components — request-logging plugins — that
// record through a Writer the gateway supplies at startup, rather than opening
// a store of their own. The supplied Writer is owned by the gateway and must
// not be closed by the receiver.
type WriterReceiver interface {
	SetRequestLogWriter(Writer)
}

// Reader loads request log entries from persistent storage.
type Reader interface {
	List(ctx context.Context, query Query) (ListResult, error)
	Stats(ctx context.Context, query Query) (StatsResult, error)
}

// Maintainer provides cleanup operations over persistent request logs.
type Maintainer interface {
	Delete(ctx context.Context, query MaintenanceQuery) (int, error)
}

// NoopWriter ignores all log writes.
type NoopWriter struct{}

func (NoopWriter) Write(_ context.Context, _ Entry) error { return nil }

// SQLWriter persists entries to SQLite/Postgres.
type SQLWriter struct {
	db      *sql.DB
	dialect sqldb.Dialect
}

// NewSQLiteWriter creates a SQLite-backed request log writer.
func NewSQLiteWriter(ctx context.Context, dsn string) (*SQLWriter, error) {
	db, err := sqldb.Open(ctx, sqldb.SQLite, dsn, "ferrogw-requests.db")
	if err != nil {
		return nil, err
	}
	w := &SQLWriter{db: db, dialect: sqldb.SQLite}
	if err := w.init(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return w, nil
}

// NewPostgresWriter creates a Postgres-backed request log writer.
func NewPostgresWriter(ctx context.Context, dsn string) (*SQLWriter, error) {
	db, err := sqldb.Open(ctx, sqldb.Postgres, dsn, "")
	if err != nil {
		return nil, err
	}
	w := &SQLWriter{db: db, dialect: sqldb.Postgres}
	if err := w.init(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return w, nil
}

func (w *SQLWriter) init(ctx context.Context) error {
	if err := migrations.RunNamed(ctx, w.db, w.dialect, requestLogLedger, "request_logs", requestLogSteps(w.dialect)); err != nil {
		return fmt.Errorf("migrate %s request log schema: %w", w.dialect, err)
	}
	return nil
}

func (w *SQLWriter) Write(ctx context.Context, entry Entry) error {
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now().UTC()
	}
	// Normalised, not merely defaulted. The SQLite driver stores a time.Time as
	// Go's rendering of it, zone offset and all, and every range filter here is
	// a string comparison over that text. Those comparisons are chronologically
	// correct only while every row carries the same offset, so a caller passing
	// a local time would write rows that sort into the wrong window.
	entry.CreatedAt = entry.CreatedAt.UTC()

	// Redact here, at the store, rather than trusting each caller to have done
	// it. A persisted row outlives the process and is served back over
	// GET /admin/logs, so it is the sink with the longest reach; putting the
	// call at the writer means a component that records a raw upstream error —
	// including one added later — cannot durably store a credential.
	entry.ErrorMessage = redact.String(entry.ErrorMessage)

	query := sqldb.Bind(w.dialect, `INSERT INTO request_logs(trace_id, stage, model, provider, api_key_id, prompt_tokens, completion_tokens, total_tokens, error_message, created_at, duration_ms, ttft_ms, cost_usd)
	VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)

	// #nosec G701 -- query is a fixed literal routed through sqldb.Bind; every value is a bound parameter.
	_, err := w.db.ExecContext(ctx, query,
		entry.TraceID,
		entry.Stage,
		entry.Model,
		entry.Provider,
		entry.APIKeyID,
		entry.PromptTokens,
		entry.CompletionTokens,
		entry.TotalTokens,
		entry.ErrorMessage,
		entry.CreatedAt,
		// A nil *float64 binds as NULL, which is the point: see Entry.
		entry.DurationMs,
		entry.TTFTMs,
		entry.CostUSD,
	)
	if err != nil {
		return fmt.Errorf("write request log: %w", err)
	}
	return nil
}

// AnnotateError implements ErrorAnnotator: it records message on the row this
// trace already wrote at stage.
//
// A blank traceID matches nothing rather than every unattributed row, which is
// what an unguarded equality against ” would update. Only a row that carries
// no message yet is touched, so the first failure recorded against a request
// stands and a retry of this path cannot rewrite it.
func (w *SQLWriter) AnnotateError(ctx context.Context, traceID, stage, message string) (bool, error) {
	if traceID == "" {
		return false, nil
	}

	// Redacted here for the same reason Write redacts: this is the sink with
	// the longest reach, and a caller that hands over a raw upstream error must
	// not be able to durably store a credential.
	query := sqldb.Bind(w.dialect, `UPDATE request_logs SET error_message = ?
	WHERE trace_id = ? AND stage = ? AND (error_message IS NULL OR error_message = '')`)

	result, err := w.db.ExecContext(ctx, query, redact.String(message), traceID, stage)
	if err != nil {
		return false, fmt.Errorf("annotate request log error: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("annotate request log rows affected: %w", err)
	}
	return affected > 0, nil
}

// List returns paginated request log entries with optional filters.
func (w *SQLWriter) List(ctx context.Context, query Query) (ListResult, error) {
	if query.Limit <= 0 {
		query.Limit = defaultListLimit
	}
	if query.Limit > maxListLimit {
		query.Limit = maxListLimit
	}
	if query.Offset < 0 {
		query.Offset = 0
	}

	whereClauses := make([]string, 0)
	args := make([]any, 0)

	if query.Stage != "" {
		whereClauses = append(whereClauses, "stage = ?")
		args = append(args, query.Stage)
	}
	if len(query.Stages) > 0 {
		// #nosec G202 -- the placeholder run is generated from the argument
		// count, never from the values themselves.
		whereClauses = append(whereClauses, "stage IN ("+strings.TrimSuffix(strings.Repeat("?,", len(query.Stages)), ",")+")")
		for _, stage := range query.Stages {
			args = append(args, stage)
		}
	}
	if query.Model != "" {
		whereClauses = append(whereClauses, "model = ?")
		args = append(args, query.Model)
	}
	if query.Provider != "" {
		whereClauses = append(whereClauses, "provider = ?")
		args = append(args, query.Provider)
	}
	if query.APIKeyID != nil {
		if *query.APIKeyID == "" {
			// NULL is not compared by `= ''` on either backend, so both halves
			// of "names no credential" have to be named. See Query.APIKeyID.
			whereClauses = append(whereClauses, "(api_key_id IS NULL OR api_key_id = '')")
		} else {
			whereClauses = append(whereClauses, "api_key_id = ?")
			args = append(args, *query.APIKeyID)
		}
	}
	if query.Since != nil {
		whereClauses = append(whereClauses, "created_at >= ?")
		args = append(args, query.Since.UTC())
	}

	whereSQL := ""
	if len(whereClauses) > 0 {
		whereSQL = " WHERE " + strings.Join(whereClauses, " AND ")
	}

	// #nosec G202 G701 -- whereSQL is built only from fixed predicates; every value is a bound placeholder.
	countQuery := sqldb.Bind(w.dialect, "SELECT COUNT(*) FROM request_logs"+whereSQL)

	var total int
	// #nosec G701 -- countQuery is assembled from fixed predicates and bound placeholders.
	if err := w.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return ListResult{}, fmt.Errorf("count request logs: %w", err)
	}

	// The id tiebreak keeps paging stable across rows written in the same clock
	// tick — which one request's own before_request and terminal rows are —
	// because created_at alone leaves their order to the planner, which can
	// repeat one row on one page and drop another.
	// #nosec G202 -- whereSQL is built only from fixed predicates and bound placeholders.
	listQuery := sqldb.Bind(w.dialect, "SELECT trace_id, stage, model, provider, api_key_id, prompt_tokens, completion_tokens, total_tokens, error_message, created_at, duration_ms, ttft_ms, cost_usd FROM request_logs"+whereSQL+" ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?")
	listArgs := make([]any, 0, len(args)+2)
	listArgs = append(listArgs, args...)
	listArgs = append(listArgs, query.Limit, query.Offset)

	// #nosec G701 -- listQuery is assembled from fixed predicates and bound placeholders.
	rows, err := w.db.QueryContext(ctx, listQuery, listArgs...)
	if err != nil {
		return ListResult{}, fmt.Errorf("list request logs: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	entries := make([]Entry, 0)
	for rows.Next() {
		var (
			e          Entry
			traceID    sql.NullString
			model      sql.NullString
			provider   sql.NullString
			apiKeyID   sql.NullString
			errMsg     sql.NullString
			durationMs sql.NullFloat64
			ttftMs     sql.NullFloat64
			costUSD    sql.NullFloat64
		)
		if err := rows.Scan(&traceID, &e.Stage, &model, &provider, &apiKeyID, &e.PromptTokens, &e.CompletionTokens, &e.TotalTokens, &errMsg, &e.CreatedAt,
			&durationMs, &ttftMs, &costUSD); err != nil {
			return ListResult{}, fmt.Errorf("scan request log row: %w", err)
		}
		if apiKeyID.Valid {
			e.APIKeyID = apiKeyID.String
		}
		e.DurationMs = nullableFloat(durationMs)
		e.TTFTMs = nullableFloat(ttftMs)
		e.CostUSD = nullableFloat(costUSD)
		if traceID.Valid {
			e.TraceID = traceID.String
		}
		if model.Valid {
			e.Model = model.String
		}
		if provider.Valid {
			e.Provider = provider.String
		}
		if errMsg.Valid {
			e.ErrorMessage = errMsg.String
		}
		entries = append(entries, e)
	}

	if err := rows.Err(); err != nil {
		return ListResult{}, fmt.Errorf("iterate request logs: %w", err)
	}

	return ListResult{Data: entries, Total: total}, nil
}

// statsQueryTemplate aggregates matching rows across three dimensions in one
// round trip. SQLite lacks GROUPING SETS, so a UNION ALL stands in. The %[1]s
// verb is the shared WHERE clause; its bound args repeat once per branch.
// COALESCE(NULLIF(col,”),'unknown') folds NULL and ” into a single group so
// nullable provider/model columns match the historical Go aggregation.
//
// The %[2]s verb is the one-row-per-request narrowing (see requestRowsOnly) and
// applies to the provider and model branches alone. The stage branch counts
// every row, because the stage breakdown is the one answer that is about rows.
// Without it a dimension counted the same request once per stage it logged, so
// five requests reported five under the model they were sent as AND five again
// under the model the provider answered with — the same double count that made
// the summary read twice the traffic, one aggregate further down.
const statsQueryTemplate = `SELECT 'stage' AS dim,
       COALESCE(NULLIF(stage, ''), 'unknown') AS grp,
       COUNT(*) AS cnt,
       SUM(CASE WHEN (error_message IS NOT NULL AND error_message <> '') OR stage = 'on_error' THEN 1 ELSE 0 END) AS errs,
       COALESCE(SUM(total_tokens), 0) AS toks,
       COALESCE(SUM(prompt_tokens), 0) AS ptoks,
       COALESCE(SUM(completion_tokens), 0) AS ctoks,
       COALESCE(SUM(cost_usd), 0) AS cost,
       SUM(CASE WHEN cost_usd IS NULL AND stage <> 'before_request' THEN 1 ELSE 0 END) AS unpriced
FROM request_logs%[1]s
GROUP BY COALESCE(NULLIF(stage, ''), 'unknown')
UNION ALL
SELECT 'provider', COALESCE(NULLIF(provider, ''), 'unknown'), COUNT(*),
       SUM(CASE WHEN (error_message IS NOT NULL AND error_message <> '') OR stage = 'on_error' THEN 1 ELSE 0 END),
       COALESCE(SUM(total_tokens), 0),
       COALESCE(SUM(prompt_tokens), 0),
       COALESCE(SUM(completion_tokens), 0),
       COALESCE(SUM(cost_usd), 0),
       SUM(CASE WHEN cost_usd IS NULL AND stage <> 'before_request' THEN 1 ELSE 0 END)
FROM request_logs%[1]s%[2]s
GROUP BY COALESCE(NULLIF(provider, ''), 'unknown')
UNION ALL
SELECT 'model', COALESCE(NULLIF(model, ''), 'unknown'), COUNT(*),
       SUM(CASE WHEN (error_message IS NOT NULL AND error_message <> '') OR stage = 'on_error' THEN 1 ELSE 0 END),
       COALESCE(SUM(total_tokens), 0),
       COALESCE(SUM(prompt_tokens), 0),
       COALESCE(SUM(completion_tokens), 0),
       COALESCE(SUM(cost_usd), 0),
       SUM(CASE WHEN cost_usd IS NULL AND stage <> 'before_request' THEN 1 ELSE 0 END)
FROM request_logs%[1]s%[2]s
GROUP BY COALESCE(NULLIF(model, ''), 'unknown')`

// topErrorsQueryTemplate ranks distinct failure messages. Ordered and limited
// in SQL rather than in Go: error text is unbounded in cardinality, so a
// gateway failing in many distinct ways would otherwise stream every variant
// back to be sorted and thrown away.
const topErrorsQueryTemplate = `SELECT error_message, COUNT(*) AS cnt
FROM request_logs%[1]s
GROUP BY error_message
ORDER BY cnt DESC, error_message ASC
LIMIT ?`

// seriesQueryTemplate streams the rows a time series is bucketed from, newest
// first so a truncated scan keeps the most recent window rather than an
// arbitrary one.
const seriesQueryTemplate = `SELECT created_at, stage, prompt_tokens, completion_tokens, error_message, duration_ms, ttft_ms
FROM request_logs%[1]s
ORDER BY created_at DESC
LIMIT ?`

// Stats aggregates request logs matching the query filters (Stage, Model,
// Provider, Since) entirely in SQL. Limit and Offset are ignored. Query.Stages
// is not applied here — the stage breakdown is the point of ByStage, so the
// summary narrows instead of the query. Returned maps are always non-nil.
//
// ByStage covers every matching row. The summary totals count each request
// once; see summarises.
func (w *SQLWriter) Stats(ctx context.Context, query Query) (StatsResult, error) {
	whereClauses := make([]string, 0)
	args := make([]any, 0)

	if query.Stage != "" {
		whereClauses = append(whereClauses, "stage = ?")
		args = append(args, query.Stage)
	}
	if query.Model != "" {
		whereClauses = append(whereClauses, "model = ?")
		args = append(args, query.Model)
	}
	if query.Provider != "" {
		whereClauses = append(whereClauses, "provider = ?")
		args = append(args, query.Provider)
	}
	if query.Since != nil {
		whereClauses = append(whereClauses, "created_at >= ?")
		args = append(args, query.Since.UTC())
	}

	whereSQL := ""
	if len(whereClauses) > 0 {
		whereSQL = " WHERE " + strings.Join(whereClauses, " AND ")
	}

	// #nosec G201 -- dimension/column names are fixed literals; whereSQL contains only bound placeholders.
	statsQuery := fmt.Sprintf(statsQueryTemplate, whereSQL, requestRowsOnly(query, whereSQL != ""))

	// whereSQL's placeholders appear once per UNION ALL branch, so its args bind
	// three times in branch order. sqldb.Bind renumbers ? sequentially across the
	// whole statement, keeping the tripled args aligned.
	allArgs := make([]any, 0, len(args)*3)
	allArgs = append(allArgs, args...)
	allArgs = append(allArgs, args...)
	allArgs = append(allArgs, args...)

	statsQuery = sqldb.Bind(w.dialect, statsQuery)

	rows, err := w.db.QueryContext(ctx, statsQuery, allArgs...)
	if err != nil {
		return StatsResult{}, fmt.Errorf("aggregate request log stats: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	result := StatsResult{
		ByStage:    map[string]DimensionStat{},
		ByProvider: map[string]DimensionStat{},
		ByModel:    map[string]DimensionStat{},
	}
	for rows.Next() {
		var (
			dim      string
			grp      string
			cnt      int
			errs     int
			toks     int
			ptoks    int
			ctoks    int
			cost     float64
			unpriced int
		)
		if err := rows.Scan(&dim, &grp, &cnt, &errs, &toks, &ptoks, &ctoks, &cost, &unpriced); err != nil {
			return StatsResult{}, fmt.Errorf("scan request log stats row: %w", err)
		}
		stat := DimensionStat{Count: cnt, Errors: errs, Tokens: toks, CostUSD: cost, Unpriced: unpriced}
		switch dim {
		case "stage":
			result.ByStage[grp] = stat
			if summarises(query, grp) {
				result.TotalEntries += cnt
				result.ErrorEntries += errs
				result.TotalTokens += toks
				result.PromptTokens += ptoks
				result.CompletionTokens += ctoks
				result.TotalCostUSD += cost
				result.UnpricedRequests += unpriced
			}
		case "provider":
			result.ByProvider[grp] = stat
		case "model":
			result.ByModel[grp] = stat
		}
	}
	if err := rows.Err(); err != nil {
		return StatsResult{}, fmt.Errorf("iterate request log stats: %w", err)
	}

	if query.TopErrors > 0 {
		topErrors, err := w.topErrors(ctx, whereSQL, args, query.TopErrors)
		if err != nil {
			return StatsResult{}, err
		}
		result.TopErrors = topErrors
	}

	if query.SeriesBuckets > 0 && query.Since != nil {
		if err := w.appendSeries(ctx, &result, whereSQL, args, query); err != nil {
			return StatsResult{}, err
		}
	}

	return result, nil
}

// summarises reports whether a stage group contributes to the summary totals.
//
// The logger writes one row per stage, so summing every group counts each
// request roughly twice — which is what TotalEntries used to report, and what
// made a gateway failing every request show a 50% error rate. Counting only the
// stages at which a request reached an outcome counts it once.
//
// A query naming one stage is exempt: there the caller has asked about that
// stage specifically, every matching row is a distinct request, and answering
// zero for `stage=before_request` would report an idle gateway. The rule is
// therefore "each request once", not "terminal stages only".
func summarises(query Query, stage string) bool {
	if query.Stage != "" {
		return true
	}
	return isTerminalStage(stage)
}

// requestRowsOnly is summarises' SQL half: the predicate that keeps one row per
// request in the provider and model breakdowns, so their counts mean the same
// thing the summary's do.
//
// Same rule, same exemption — a query naming one stage has asked about that
// stage, so every matching row is a distinct request and nothing is narrowed.
// The stage names are compile-time constants from TerminalStages, so they are
// written into the statement rather than bound: the three UNION branches share
// one argument list, and a placeholder here would have to be threaded through
// all of it to reach one branch.
func requestRowsOnly(query Query, hasWhere bool) string {
	if query.Stage != "" {
		return ""
	}
	clause := "stage IN ('" + strings.Join(TerminalStages(), "', '") + "')"
	if hasWhere {
		return " AND " + clause
	}
	return " WHERE " + clause
}

// topErrors ranks the distinct failure messages matching the same filters.
//
// The empty message is excluded here rather than in the template so the caller's
// bound arguments stay in one slice; a successful row carries no message and
// would otherwise rank first by a wide margin.
func (w *SQLWriter) topErrors(ctx context.Context, whereSQL string, args []any, limit int) ([]ErrorGroup, error) {
	errorFilter := " WHERE error_message IS NOT NULL AND error_message <> ''"
	if whereSQL != "" {
		errorFilter = whereSQL + " AND error_message IS NOT NULL AND error_message <> ''"
	}

	// #nosec G201 -- the filter is assembled from fixed predicates; every value is a bound placeholder.
	query := sqldb.Bind(w.dialect, fmt.Sprintf(topErrorsQueryTemplate, errorFilter))

	queryArgs := make([]any, 0, len(args)+1)
	queryArgs = append(queryArgs, args...)
	queryArgs = append(queryArgs, limit)

	rows, err := w.db.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return nil, fmt.Errorf("rank request log errors: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	groups := make([]ErrorGroup, 0, limit)
	for rows.Next() {
		var group ErrorGroup
		if err := rows.Scan(&group.Message, &group.Count); err != nil {
			return nil, fmt.Errorf("scan request log error group: %w", err)
		}
		groups = append(groups, group)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate request log error groups: %w", err)
	}
	return groups, nil
}

// appendSeries builds the time series by bucketing scanned rows in Go.
//
// See seriesScanLimit for why this is not a SQL GROUP BY on a date expression.
func (w *SQLWriter) appendSeries(ctx context.Context, result *StatsResult, whereSQL string, args []any, query Query) error {
	buckets := min(query.SeriesBuckets, maxSeriesBuckets)

	// #nosec G201 -- whereSQL is assembled from fixed predicates; every value is a bound placeholder.
	seriesQuery := sqldb.Bind(w.dialect, fmt.Sprintf(seriesQueryTemplate, whereSQL))

	queryArgs := make([]any, 0, len(args)+1)
	queryArgs = append(queryArgs, args...)
	// One more than the cap, so a full scan is distinguishable from one that
	// happens to land exactly on it.
	queryArgs = append(queryArgs, seriesScanLimit+1)

	rows, err := w.db.QueryContext(ctx, seriesQuery, queryArgs...)
	if err != nil {
		return fmt.Errorf("scan request log series: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	type event struct {
		at               time.Time
		requested        bool
		failed           bool
		promptTokens     int
		completionTokens int
	}

	// Percentiles ride along on this scan rather than taking a query of their
	// own. Neither backend offers a percentile both can run — SQLite has no
	// such function at all — and the rows are already being read.
	var durations, ttfts []float64

	events := make([]event, 0, 1024)
	for rows.Next() {
		var (
			at           time.Time
			stage        string
			promptTokens int
			outputTokens int
			errMessage   sql.NullString
			durationMs   sql.NullFloat64
			ttftMs       sql.NullFloat64
		)
		if err := rows.Scan(&at, &stage, &promptTokens, &outputTokens, &errMessage, &durationMs, &ttftMs); err != nil {
			return fmt.Errorf("scan request log series row: %w", err)
		}
		if durationMs.Valid {
			durations = append(durations, durationMs.Float64)
		}
		if ttftMs.Valid {
			ttfts = append(ttfts, ttftMs.Float64)
		}
		if len(events) == seriesScanLimit {
			result.SeriesTruncated = true
			break
		}
		events = append(events, event{
			at: at.UTC(),
			// Only a terminal stage counts as a request; before_request rows are
			// the same requests counted a second time.
			requested:        stage == stageAfterRequest || stage == stageOnError,
			failed:           stage == stageOnError || errMessage.String != "",
			promptTokens:     promptTokens,
			completionTokens: outputTokens,
		})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate request log series: %w", err)
	}

	start := query.Since.UTC()
	end := time.Now().UTC()
	if result.SeriesTruncated && len(events) > 0 {
		// The scan stopped short of the requested window, so the window it did
		// cover starts at the oldest row it reached. Plotting from the
		// requested start would render everything before that as zero traffic.
		start = events[len(events)-1].at
	}
	if !end.After(start) {
		end = start.Add(time.Second)
	}

	width := end.Sub(start) / time.Duration(buckets)
	if width <= 0 {
		width = time.Second
	}

	points := make([]SeriesPoint, buckets)
	for i := range points {
		points[i].Start = start.Add(time.Duration(i) * width)
	}
	for _, e := range events {
		index := int(e.at.Sub(start) / width)
		if index < 0 || index >= buckets {
			continue
		}
		if e.requested {
			points[index].Requests++
			if e.failed {
				points[index].Errors++
			}
		}
		points[index].PromptTokens += e.promptTokens
		points[index].CompletionTokens += e.completionTokens
	}

	result.Series = points
	result.SeriesStart = start
	result.SeriesEnd = end
	result.LatencyMs = summarise(durations)
	result.TTFTMs = summarise(ttfts)
	return nil
}

// summarise reduces measurements to percentiles, or nil when there are none.
//
// Nil rather than a zero-valued struct: a gateway that has recorded no duration
// has no median, and reporting 0ms would read as an implausibly fast one. Rows
// written before the columns existed, and non-streaming requests in the case of
// TTFT, legitimately carry nothing.
func summarise(values []float64) *Percentiles {
	if len(values) == 0 {
		return nil
	}
	sorted := slices.Clone(values)
	slices.Sort(sorted)

	var sum float64
	for _, value := range sorted {
		sum += value
	}

	return &Percentiles{
		P50:   nearestRank(sorted, 0.50),
		P95:   nearestRank(sorted, 0.95),
		P99:   nearestRank(sorted, 0.99),
		Max:   sorted[len(sorted)-1],
		Mean:  sum / float64(len(sorted)),
		Count: len(sorted),
	}
}

// nearestRank picks the value at the given quantile of a sorted slice.
//
// Nearest-rank rather than an interpolating variant: an interpolated p95 is a
// number no request actually took, which is a strange thing to show beside a
// list of real ones. The two agree closely at any sample size worth reading.
func nearestRank(sorted []float64, quantile float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(math.Ceil(quantile * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

// Delete removes request log entries matching maintenance filters.
func (w *SQLWriter) Delete(ctx context.Context, query MaintenanceQuery) (int, error) {
	if query.Before == nil {
		return 0, fmt.Errorf("before is required")
	}

	whereClauses := []string{"created_at < ?"}
	args := []any{query.Before.UTC()}

	if query.Stage != "" {
		whereClauses = append(whereClauses, "stage = ?")
		args = append(args, query.Stage)
	}
	if query.Model != "" {
		whereClauses = append(whereClauses, "model = ?")
		args = append(args, query.Model)
	}
	if query.Provider != "" {
		whereClauses = append(whereClauses, "provider = ?")
		args = append(args, query.Provider)
	}

	// #nosec G202 -- delete predicates are assembled from a fixed allowlist with placeholders.
	deleteQuery := sqldb.Bind(w.dialect, "DELETE FROM request_logs WHERE "+strings.Join(whereClauses, " AND "))

	result, err := w.db.ExecContext(ctx, deleteQuery, args...)
	if err != nil {
		return 0, fmt.Errorf("delete request logs: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("delete request logs rows affected: %w", err)
	}

	return int(affected), nil
}

// Close closes the underlying SQL connection.
func (w *SQLWriter) Close() error {
	if w == nil || w.db == nil {
		return nil
	}
	return w.db.Close()
}

// nullableFloat converts a scanned NULL into a nil pointer, preserving the
// distinction between "not measured" and "measured as zero". See Entry.
func nullableFloat(value sql.NullFloat64) *float64 {
	if !value.Valid {
		return nil
	}
	measured := value.Float64
	return &measured
}
