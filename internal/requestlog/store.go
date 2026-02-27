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

// Writer persists request log entries.
type Writer interface {
	Write(ctx context.Context, entry Entry) error
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

func (w *SQLWriter) Close() error {
	if w == nil || w.db == nil {
		return nil
	}
	return w.db.Close()
}
