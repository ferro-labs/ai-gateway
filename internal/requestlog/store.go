package requestlog

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/lib/pq"
	_ "modernc.org/sqlite"
)

// Entry represents a persistent request log event emitted by logging plugins.
type Entry struct {
	TraceID          string
	Stage            string
	Model            string
	Provider         string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	ErrorMessage     string
	CreatedAt        time.Time
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

// ListResult is a paginated request log query response.
type ListResult struct {
	Data  []Entry
	Total int
}

// Writer persists request log entries.
type Writer interface {
	Write(ctx context.Context, entry Entry) error
}

// Reader loads request log entries from persistent storage.
type Reader interface {
	List(ctx context.Context, query Query) (ListResult, error)
}

// NoopWriter ignores all log writes.
type NoopWriter struct{}

func (NoopWriter) Write(_ context.Context, _ Entry) error { return nil }

// SQLWriter persists entries to SQLite/Postgres.
type SQLWriter struct {
	db      *sql.DB
	dialect string
}

func NewSQLiteWriter(dsn string) (*SQLWriter, error) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		dsn = "ferrogw-requests.db"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite request log writer: %w", err)
	}
	w := &SQLWriter{db: db, dialect: "sqlite"}
	if err := w.init(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return w, nil
}

func NewPostgresWriter(dsn string) (*SQLWriter, error) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return nil, fmt.Errorf("postgres dsn is required")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres request log writer: %w", err)
	}
	w := &SQLWriter{db: db, dialect: "postgres"}
	if err := w.init(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return w, nil
}

func (w *SQLWriter) init() error {
	if err := w.db.Ping(); err != nil {
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

	if w.dialect == "postgres" {
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

	if _, err := w.db.Exec(ddl); err != nil {
		return fmt.Errorf("initialize request log schema: %w", err)
	}
	return nil
}

func (w *SQLWriter) Write(ctx context.Context, entry Entry) error {
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now().UTC()
	}

	query := `INSERT INTO request_logs(trace_id, stage, model, provider, prompt_tokens, completion_tokens, total_tokens, error_message, created_at)
	VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`
	if w.dialect == "postgres" {
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
		query.Limit = 50
	}
	if query.Limit > 200 {
		query.Limit = 200
	}
	if query.Offset < 0 {
		query.Offset = 0
	}

	whereClauses := make([]string, 0)
	args := make([]interface{}, 0)

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
	if w.dialect == "postgres" {
		countQuery = bindPostgres(countQuery)
	}

	var total int
	if err := w.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return ListResult{}, fmt.Errorf("count request logs: %w", err)
	}

	listQuery := "SELECT trace_id, stage, model, provider, prompt_tokens, completion_tokens, total_tokens, error_message, created_at FROM request_logs" + whereSQL + " ORDER BY created_at DESC LIMIT ? OFFSET ?"
	listArgs := append(args, query.Limit, query.Offset)
	if w.dialect == "postgres" {
		listQuery = bindPostgres(listQuery)
	}

	rows, err := w.db.QueryContext(ctx, listQuery, listArgs...)
	if err != nil {
		return ListResult{}, fmt.Errorf("list request logs: %w", err)
	}
	defer rows.Close()

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

func bindPostgres(query string) string {
	var (
		builder strings.Builder
		index   = 1
	)
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			builder.WriteString(fmt.Sprintf("$%d", index))
			index++
			continue
		}
		builder.WriteByte(query[i])
	}
	return builder.String()
}

func (w *SQLWriter) Close() error {
	if w == nil || w.db == nil {
		return nil
	}
	return w.db.Close()
}
