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

	"github.com/ferro-labs/ai-gateway/internal/sqldb"
)

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
	// Clamp here rather than trusting the caller. Stats gates this call on
	// SeriesBuckets > 0, but that guard sits one level up and a second caller
	// would silently lose it: zero divides by zero deriving the bucket width
	// below, and a negative panics in make. SeriesBuckets is request-supplied,
	// so the bound belongs where the value is used.
	buckets := query.SeriesBuckets
	if buckets < 1 {
		buckets = 1
	}
	if buckets > maxSeriesBuckets {
		buckets = maxSeriesBuckets
	}

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

	// Allocated at the constant ceiling, then filled to the clamped count, so
	// the allocation size is a compile-time constant rather than anything
	// derived from the request. buckets is already bounded above by
	// maxSeriesBuckets, so the append never grows this beyond its capacity.
	points := make([]SeriesPoint, 0, maxSeriesBuckets)
	for i := range buckets {
		points = append(points, SeriesPoint{Start: start.Add(time.Duration(i) * width)})
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
