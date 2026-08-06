package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/admin/model"
	"github.com/ferro-labs/ai-gateway/internal/migrations"
	"github.com/ferro-labs/ai-gateway/internal/sqldb"
	"github.com/ferro-labs/ai-gateway/pkg/logger"
)

// SQLSessionStore persists sessions in SQLite or Postgres. Both dialects carry
// the same logical schema; only the timestamp type differs.
type SQLSessionStore struct {
	db      *sql.DB
	dialect sqldb.Dialect
}

// NewSQLSessionStore runs the sessions migrations and returns a ready store.
func NewSQLSessionStore(ctx context.Context, db *sql.DB, dialect sqldb.Dialect) (*SQLSessionStore, error) {
	if err := migrations.RunNamed(ctx, db, dialect, sessionLedger, "sessions", sessionStoreSteps(dialect)); err != nil {
		return nil, fmt.Errorf("migrate %s store schema: %w", dialect, err)
	}
	return &SQLSessionStore{db: db, dialect: dialect}, nil
}

// NewSQLiteSessionStore opens or creates the SQLite session database.
//
// This is a distinct file from the key store's: ferrogw-sessions.db, not
// ferrogw-keys.db. A SQLite key file is something operators copy between
// machines, and live session rows have no business travelling with it.
func NewSQLiteSessionStore(ctx context.Context, dsn string) (*SQLSessionStore, error) {
	db, err := sqldb.Open(ctx, sqldb.SQLite, dsn, "ferrogw-sessions.db")
	if err != nil {
		return nil, err
	}
	store, err := NewSQLSessionStore(ctx, db, sqldb.SQLite)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// NewPostgresSessionStore connects to the PostgreSQL session database.
//
// Unlike SQLite, Postgres may share a DSN with the key store: the two
// schemas carry their own migration ledgers (see sessionLedger vs
// keyStoreLedger) and cannot collide.
func NewPostgresSessionStore(ctx context.Context, dsn string) (*SQLSessionStore, error) {
	db, err := sqldb.Open(ctx, sqldb.Postgres, dsn, "")
	if err != nil {
		return nil, err
	}
	store, err := NewSQLSessionStore(ctx, db, sqldb.Postgres)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// CreateSession mints a session and persists it, returning it together with
// the plaintext token, which is available only here and never recoverable
// afterwards.
func (s *SQLSessionStore) CreateSession(ctx context.Context, subject, credentialID string, scopes []string, ttl time.Duration) (*model.Session, string, error) {
	token, err := generateSessionToken()
	if err != nil {
		return nil, "", err
	}
	id, err := generateSessionID()
	if err != nil {
		return nil, "", err
	}
	encodedScopes, err := json.Marshal(scopes)
	if err != nil {
		return nil, "", fmt.Errorf("encode scopes: %w", err)
	}

	now := time.Now().UTC()
	expires := now.Add(ttl)

	s.sweepExpiredSessions(ctx, now)

	query := sqldb.Bind(s.dialect,
		"INSERT INTO sessions(id, token_hash, subject, credential_id, scopes, created_at, expires_at) VALUES(?, ?, ?, ?, ?, ?, ?)")
	if _, err := s.db.ExecContext(ctx, query, id, hashSessionToken(token), subject, credentialID, string(encodedScopes), now, expires); err != nil {
		return nil, "", fmt.Errorf("create session: %w", err)
	}

	return &model.Session{
		ID:           id,
		CredentialID: credentialID,
		Subject:      subject,
		Scopes:       append([]string(nil), scopes...),
		CreatedAt:    now,
		ExpiresAt:    expires,
	}, token, nil
}

// scanSession reads one row in the column order every query below uses. The
// row is freshly scanned into a local Session on every call, so the result
// needs no further cloning before it leaves the store.
func scanSession(row interface{ Scan(...any) error }) (*model.Session, error) {
	var (
		sess       model.Session
		rawScopes  string
		lastSeenAt sql.NullTime
	)
	if err := row.Scan(&sess.ID, &sess.Subject, &sess.CredentialID, &rawScopes, &sess.CreatedAt, &lastSeenAt, &sess.ExpiresAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(rawScopes), &sess.Scopes); err != nil {
		return nil, err
	}
	if lastSeenAt.Valid {
		t := lastSeenAt.Time.UTC()
		sess.LastSeenAt = &t
	}
	sess.CreatedAt = sess.CreatedAt.UTC()
	sess.ExpiresAt = sess.ExpiresAt.UTC()
	return &sess, nil
}

// ValidateSession resolves a plaintext token, refreshing last-seen. It reports
// false for an unknown, expired, or idle-expired token without distinguishing
// between them.
func (s *SQLSessionStore) ValidateSession(ctx context.Context, token string) (*model.Session, bool) {
	if token == "" {
		return nil, false
	}
	query := sqldb.Bind(s.dialect,
		"SELECT id, subject, credential_id, scopes, created_at, last_seen_at, expires_at FROM sessions WHERE token_hash = ?")
	sess, err := scanSession(s.db.QueryRowContext(ctx, query, hashSessionToken(token)))
	if err != nil {
		return nil, false
	}

	now := time.Now().UTC()
	// Both bounds are enforced here rather than by a sweeper, so a row that
	// outlived either one never validates even if no sweep has run.
	if !isLive(sess, now) {
		return nil, false
	}

	// Not every validated request writes; see sessionLastSeenResolution. When
	// the write is skipped the stored value is returned unchanged rather than
	// being reported as now, so ListSessions shows what is actually persisted.
	if !shouldRefreshLastSeen(sess, now) {
		return sess, true
	}

	update := sqldb.Bind(s.dialect, "UPDATE sessions SET last_seen_at = ? WHERE id = ?")
	if _, err := s.db.ExecContext(ctx, update, now, sess.ID); err != nil {
		// Refreshing last-seen is bookkeeping. Failing the request over it
		// would turn a transient write error into a sign-out, so the
		// authenticated request proceeds and the idle clock simply does not
		// advance this time.
		logger.Default().Warn("session last_seen update failed", "error", err)
		return sess, true
	}
	sess.LastSeenAt = &now
	return sess, true
}

// sweepExpiredSessions deletes rows that can no longer validate.
//
// The predicate is isLive's negation, restated in SQL: past the absolute bound,
// or idle since at or before the cutoff. Enforcement stays at lookup — this only
// stops the table growing without limit, and stops ListSessions reading rows it
// will discard.
//
// It runs on create for the reason MemorySessionStore.sweep gives, which matters
// more here: on SQLite the pool is one connection, so a background sweeper would
// contend directly with the request path.
//
// A failure is logged and swallowed. Reclaiming space must never be the reason
// an operator cannot sign in.
func (s *SQLSessionStore) sweepExpiredSessions(ctx context.Context, now time.Time) {
	query := sqldb.Bind(s.dialect,
		"DELETE FROM sessions WHERE expires_at <= ? OR COALESCE(last_seen_at, created_at) <= ?")
	if _, err := s.db.ExecContext(ctx, query, now, idleCutoff(now)); err != nil {
		logger.Default().Warn("expired session sweep failed", "error", err)
	}
}

// ListSessions returns every currently live session, newest first.
//
// The id tiebreak keeps two sessions minted in the same clock tick — which two
// browser tabs signing in together produce — in a fixed order; created_at alone
// leaves that to the planner, so an unchanged table can list them differently on
// consecutive polls. MemorySessionStore.ListSessions sorts by the same two
// fields.
func (s *SQLSessionStore) ListSessions(ctx context.Context) ([]*model.Session, error) {
	query := sqldb.Bind(s.dialect,
		"SELECT id, subject, credential_id, scopes, created_at, last_seen_at, expires_at FROM sessions ORDER BY created_at DESC, id DESC")
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	now := time.Now().UTC()
	out := make([]*model.Session, 0)
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		if !isLive(sess, now) {
			continue
		}
		out = append(out, sess)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sessions: %w", err)
	}
	return out, nil
}

// DeleteSession removes the session with the given id.
func (s *SQLSessionStore) DeleteSession(ctx context.Context, id string) error {
	query := sqldb.Bind(s.dialect, "DELETE FROM sessions WHERE id = ?")
	res, err := s.db.ExecContext(ctx, query, id)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	if affected == 0 {
		return ErrSessionNotFound
	}
	return nil
}

// DeleteAllSessions removes every session and returns how many were removed.
// It is the "sign everyone out" control.
func (s *SQLSessionStore) DeleteAllSessions(ctx context.Context) (int, error) {
	res, err := s.db.ExecContext(ctx, "DELETE FROM sessions")
	if err != nil {
		return 0, fmt.Errorf("delete all sessions: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("delete all sessions: %w", err)
	}
	return int(affected), nil
}

// Close closes the underlying SQL connection, matching (*SQLStore).Close and
// (*SQLConfigStore).Close so bootstrap shutdown can close every store the
// same way regardless of which one it is.
func (s *SQLSessionStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Compile-time assertion, matching the guards every provider package uses.
var _ SessionStore = (*SQLSessionStore)(nil)
