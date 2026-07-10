// Package requestlog provides persistent storage primitives for request/response logs.
package requestlog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/sqlitefile"

	// Register Postgres SQL driver.
	_ "github.com/lib/pq"
	// Register SQLite SQL driver.
	_ "modernc.org/sqlite"
)

const (
	requestLogDialectSQLite   = "sqlite"
	requestLogDialectPostgres = "postgres"

	// defaultListLimit is the page size applied when a List query omits Limit.
	defaultListLimit = 50
	// maxListLimit caps the page size a List query may request.
	maxListLimit = 200
)

// Entry represents a persistent request log event emitted by logging plugins.
type Entry struct {
	TraceID          string    `json:"trace_id" yaml:"trace_id"`
	Stage            string    `json:"stage" yaml:"stage"`
	Model            string    `json:"model" yaml:"model"`
	Provider         string    `json:"provider" yaml:"provider"`
	PromptTokens     int       `json:"prompt_tokens" yaml:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens" yaml:"completion_tokens"`
	TotalTokens      int       `json:"total_tokens" yaml:"total_tokens"`
	ErrorMessage     string    `json:"error_message" yaml:"error_message"`
	CreatedAt        time.Time `json:"created_at" yaml:"created_at"`
}

// Query defines request log listing filters.
type Query struct {
	Limit    int
	Offset   int
	Stage    string
	Model    string
	Provider string
	Since    *time.Time
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

// StatsResult is an aggregated summary of the request logs matching a Query's filters.
type StatsResult struct {
	TotalEntries int
	ErrorEntries int
	TotalTokens  int
	ByStage      map[string]int
	ByProvider   map[string]int
	ByModel      map[string]int
}

// Writer persists request log entries.
type Writer interface {
	Write(ctx context.Context, entry Entry) error
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
	dialect string
}

// NewSQLiteWriter creates a SQLite-backed request log writer.
func NewSQLiteWriter(ctx context.Context, dsn string) (*SQLWriter, error) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		dsn = "ferrogw-requests.db"
	}
	// Restrict the file before SQLite touches it: a file created under the
	// process umask is world-readable until something narrows it.
	if err := sqlitefile.Secure(dsn); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite request log writer: %w", err)
	}
	tuneDBPool(db, requestLogDialectSQLite)

	w := &SQLWriter{db: db, dialect: requestLogDialectSQLite}
	if err := w.init(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return w, nil
}

// NewPostgresWriter creates a Postgres-backed request log writer.
func NewPostgresWriter(ctx context.Context, dsn string) (*SQLWriter, error) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return nil, fmt.Errorf("postgres dsn is required")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres request log writer: %w", err)
	}
	tuneDBPool(db, requestLogDialectPostgres)
	w := &SQLWriter{db: db, dialect: requestLogDialectPostgres}
	if err := w.init(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return w, nil
}

func (w *SQLWriter) init(ctx context.Context) error {
	if err := w.db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping %s request log writer: %w", w.dialect, err)
	}

	ddl := `
CREATE TABLE IF NOT EXISTS request_logs (
	id INTEGER PRIMARY KEY,
	trace_id TEXT,
	stage TEXT NOT NULL,
	model TEXT,
	provider TEXT,
	prompt_tokens INTEGER NOT NULL,
	completion_tokens INTEGER NOT NULL,
	total_tokens INTEGER NOT NULL,
	error_message TEXT,
	created_at TIMESTAMP NOT NULL
);`

	if w.dialect == requestLogDialectPostgres {
		ddl = `
CREATE TABLE IF NOT EXISTS request_logs (
	id BIGSERIAL PRIMARY KEY,
	trace_id TEXT,
	stage TEXT NOT NULL,
	model TEXT,
	provider TEXT,
	prompt_tokens INTEGER NOT NULL,
	completion_tokens INTEGER NOT NULL,
	total_tokens INTEGER NOT NULL,
	error_message TEXT,
	created_at TIMESTAMPTZ NOT NULL
);`
	}

	if _, err := w.db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("initialize request log schema: %w", err)
	}
	return w.ensureCreatedAtIndex(ctx)
}

// createdAtIndex serves List's created_at ordering and range, Delete's
// created_at range, and Stats' since filter.
const createdAtIndex = "idx_request_logs_created_at"

// ensureCreatedAtIndex builds the created_at index if it is missing.
//
// request_logs takes a write per request per stage. On Postgres a plain
// CREATE INDEX holds a lock that blocks those writes for the length of the
// build, which on an existing table means every other instance stalls while one
// restarts. Building concurrently trades a slower build for not blocking
// writers. It cannot run inside a transaction, and this is not one.
//
// A missing index costs query time, not correctness, so a failed build is
// logged rather than fatal: the next start retries it.
func (w *SQLWriter) ensureCreatedAtIndex(ctx context.Context) error {
	if w.dialect != requestLogDialectPostgres {
		// createdAtIndex is a package constant, not input; identifiers cannot be
		// bound as parameters.
		if _, err := w.db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS "+createdAtIndex+" ON request_logs (created_at)"); err != nil {
			return fmt.Errorf("initialize request log index: %w", err)
		}
		return nil
	}

	switch w.postgresIndexState(ctx) {
	case indexValid:
		return nil
	case indexInvalid:
		// An interrupted CREATE INDEX CONCURRENTLY leaves the index in place but
		// marked invalid, and CREATE ... IF NOT EXISTS then skips it forever.
		// Healing it means dropping and rebuilding, but a bare-name check cannot
		// tell an abandoned index from one another instance is building right
		// now, and dropping that would abort the live build. So this logs and
		// defers to the operator rather than coordinating a fleet-wide drop. A
		// self-healing rebuild would need an advisory lock around the whole
		// probe/drop/create; add it only if interrupted builds prove common.
		slog.Warn("request log index is invalid from an interrupted build; run REINDEX INDEX CONCURRENTLY to rebuild it",
			"index", createdAtIndex)
		return nil
	default: // indexAbsent
		if _, err := w.db.ExecContext(ctx, "CREATE INDEX CONCURRENTLY IF NOT EXISTS "+createdAtIndex+" ON request_logs (created_at)"); err != nil {
			// A failed concurrent build usually leaves an invalid index behind,
			// which the next start reports as indexInvalid (and points at
			// REINDEX) rather than silently rebuilding. Until then queries scan.
			slog.Warn("request log index build failed; queries will scan until it is rebuilt with REINDEX INDEX CONCURRENTLY",
				"index", createdAtIndex, "error", err)
		}
		return nil
	}
}

type indexState int

const (
	indexAbsent indexState = iota
	indexValid
	indexInvalid
)

// postgresIndexState reports whether the created_at index is absent, present
// and usable, or present but invalid.
//
// to_regclass resolves the name through search_path, so the probe inspects the
// index the writer would actually use rather than a same-named index in another
// schema. It returns NULL — and the join no rows — when the index does not
// exist. A probe failure is treated as absent so a transient error at most
// triggers a redundant IF NOT EXISTS build.
func (w *SQLWriter) postgresIndexState(ctx context.Context) indexState {
	const probe = `SELECT i.indisvalid FROM pg_index i WHERE i.indexrelid = to_regclass($1)`

	var valid bool
	switch err := w.db.QueryRowContext(ctx, probe, createdAtIndex).Scan(&valid); {
	case errors.Is(err, sql.ErrNoRows):
		return indexAbsent
	case err != nil:
		slog.Warn("could not probe request log index; assuming it is absent", "index", createdAtIndex, "error", err)
		return indexAbsent
	case valid:
		return indexValid
	default:
		return indexInvalid
	}
}

func (w *SQLWriter) Write(ctx context.Context, entry Entry) error {
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now().UTC()
	}

	query := `INSERT INTO request_logs(trace_id, stage, model, provider, prompt_tokens, completion_tokens, total_tokens, error_message, created_at)
	VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`
	if w.dialect == requestLogDialectPostgres {
		query = `INSERT INTO request_logs(trace_id, stage, model, provider, prompt_tokens, completion_tokens, total_tokens, error_message, created_at)
		VALUES($1, $2, $3, $4, $5, $6, $7, $8, $9)`
	}

	_, err := w.db.ExecContext(ctx, query,
		entry.TraceID,
		entry.Stage,
		entry.Model,
		entry.Provider,
		entry.PromptTokens,
		entry.CompletionTokens,
		entry.TotalTokens,
		entry.ErrorMessage,
		entry.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("write request log: %w", err)
	}
	return nil
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

	countQuery := "SELECT COUNT(*) FROM request_logs" + whereSQL
	if w.dialect == requestLogDialectPostgres {
		countQuery = bindPostgres(countQuery)
	}

	var total int
	if err := w.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return ListResult{}, fmt.Errorf("count request logs: %w", err)
	}

	// #nosec G202 -- whereSQL is built only from fixed predicates and bound placeholders.
	listQuery := "SELECT trace_id, stage, model, provider, prompt_tokens, completion_tokens, total_tokens, error_message, created_at FROM request_logs" + whereSQL + " ORDER BY created_at DESC LIMIT ? OFFSET ?"
	listArgs := make([]any, 0, len(args)+2)
	listArgs = append(listArgs, args...)
	listArgs = append(listArgs, query.Limit, query.Offset)
	if w.dialect == requestLogDialectPostgres {
		listQuery = bindPostgres(listQuery)
	}

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
			e        Entry
			traceID  sql.NullString
			model    sql.NullString
			provider sql.NullString
			errMsg   sql.NullString
		)
		if err := rows.Scan(&traceID, &e.Stage, &model, &provider, &e.PromptTokens, &e.CompletionTokens, &e.TotalTokens, &errMsg, &e.CreatedAt); err != nil {
			return ListResult{}, fmt.Errorf("scan request log row: %w", err)
		}
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
const statsQueryTemplate = `SELECT 'stage' AS dim,
       COALESCE(NULLIF(stage, ''), 'unknown') AS grp,
       COUNT(*) AS cnt,
       SUM(CASE WHEN (error_message IS NOT NULL AND error_message <> '') OR stage = 'on_error' THEN 1 ELSE 0 END) AS errs,
       COALESCE(SUM(total_tokens), 0) AS toks
FROM request_logs%[1]s
GROUP BY COALESCE(NULLIF(stage, ''), 'unknown')
UNION ALL
SELECT 'provider', COALESCE(NULLIF(provider, ''), 'unknown'), COUNT(*),
       SUM(CASE WHEN (error_message IS NOT NULL AND error_message <> '') OR stage = 'on_error' THEN 1 ELSE 0 END),
       COALESCE(SUM(total_tokens), 0)
FROM request_logs%[1]s
GROUP BY COALESCE(NULLIF(provider, ''), 'unknown')
UNION ALL
SELECT 'model', COALESCE(NULLIF(model, ''), 'unknown'), COUNT(*),
       SUM(CASE WHEN (error_message IS NOT NULL AND error_message <> '') OR stage = 'on_error' THEN 1 ELSE 0 END),
       COALESCE(SUM(total_tokens), 0)
FROM request_logs%[1]s
GROUP BY COALESCE(NULLIF(model, ''), 'unknown')`

// Stats aggregates request logs matching the query filters (Stage, Model,
// Provider, Since) entirely in SQL. Limit and Offset are ignored. Returned maps
// are always non-nil. TotalEntries/ErrorEntries/TotalTokens are derived from the
// stage rows, which partition every matching row exactly once.
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
	statsQuery := fmt.Sprintf(statsQueryTemplate, whereSQL)

	// whereSQL's placeholders appear once per UNION ALL branch, so its args bind
	// three times in branch order. bindPostgres renumbers ? sequentially across
	// the whole statement, keeping the tripled args aligned.
	allArgs := make([]any, 0, len(args)*3)
	allArgs = append(allArgs, args...)
	allArgs = append(allArgs, args...)
	allArgs = append(allArgs, args...)

	if w.dialect == requestLogDialectPostgres {
		statsQuery = bindPostgres(statsQuery)
	}

	rows, err := w.db.QueryContext(ctx, statsQuery, allArgs...)
	if err != nil {
		return StatsResult{}, fmt.Errorf("aggregate request log stats: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	result := StatsResult{
		ByStage:    map[string]int{},
		ByProvider: map[string]int{},
		ByModel:    map[string]int{},
	}
	for rows.Next() {
		var (
			dim  string
			grp  string
			cnt  int
			errs int
			toks int
		)
		if err := rows.Scan(&dim, &grp, &cnt, &errs, &toks); err != nil {
			return StatsResult{}, fmt.Errorf("scan request log stats row: %w", err)
		}
		switch dim {
		case "stage":
			result.ByStage[grp] = cnt
			result.TotalEntries += cnt
			result.ErrorEntries += errs
			result.TotalTokens += toks
		case "provider":
			result.ByProvider[grp] = cnt
		case "model":
			result.ByModel[grp] = cnt
		}
	}
	if err := rows.Err(); err != nil {
		return StatsResult{}, fmt.Errorf("iterate request log stats: %w", err)
	}

	return result, nil
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
	deleteQuery := "DELETE FROM request_logs WHERE " + strings.Join(whereClauses, " AND ")
	if w.dialect == requestLogDialectPostgres {
		deleteQuery = bindPostgres(deleteQuery)
	}

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

func bindPostgres(query string) string {
	var (
		builder strings.Builder
		index   = 1
	)
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			fmt.Fprintf(&builder, "$%d", index)
			index++
			continue
		}
		builder.WriteByte(query[i])
	}
	return builder.String()
}

// Close closes the underlying SQL connection.
func (w *SQLWriter) Close() error {
	if w == nil || w.db == nil {
		return nil
	}
	return w.db.Close()
}
