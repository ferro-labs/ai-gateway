package admin

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	// Register Postgres SQL driver.
	_ "github.com/lib/pq"
	// Register SQLite SQL driver.
	_ "modernc.org/sqlite"
)

type sqlDialect string

const (
	dialectSQLite   sqlDialect = "sqlite"
	dialectPostgres sqlDialect = "postgres"
)

// SQLStore persists API keys in SQL backends (SQLite or Postgres).
type SQLStore struct {
	db            *sql.DB
	dialect       sqlDialect
	stmtGetByID   *sql.Stmt
	stmtGetByKey  *sql.Stmt
	stmtRevoke    *sql.Stmt
	stmtUpdate    *sql.Stmt
	stmtSetExpiry *sql.Stmt
	stmtDelete    *sql.Stmt
	stmtUsage     *sql.Stmt
	stmtRotate    *sql.Stmt
}

// NewSQLiteStore creates a SQLite-backed key store.
// dsn can be a file path (e.g. /tmp/keys.db) or SQLite DSN.
func NewSQLiteStore(dsn string) (*SQLStore, error) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		dsn = "ferrogw-keys.db"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite store: %w", err)
	}
	tuneDBPool(db, string(dialectSQLite))
	store := &SQLStore{db: db, dialect: dialectSQLite}
	if err := store.init(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// NewPostgresStore creates a Postgres-backed key store.
func NewPostgresStore(dsn string) (*SQLStore, error) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return nil, fmt.Errorf("postgres dsn is required")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres store: %w", err)
	}
	tuneDBPool(db, string(dialectPostgres))
	store := &SQLStore{db: db, dialect: dialectPostgres}
	if err := store.init(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *SQLStore) init() error {
	if err := s.db.Ping(); err != nil {
		return fmt.Errorf("ping %s store: %w", s.dialect, err)
	}

	var ddl string
	switch s.dialect {
	case dialectPostgres:
		ddl = `
CREATE TABLE IF NOT EXISTS api_keys (
	id TEXT PRIMARY KEY,
	key TEXT UNIQUE NOT NULL,
	name TEXT NOT NULL,
	scopes TEXT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL,
	revoked_at TIMESTAMPTZ NULL,
	expires_at TIMESTAMPTZ NULL,
	rotated_at TIMESTAMPTZ NULL,
	active BOOLEAN NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_api_keys_key ON api_keys(key);`
	default:
		ddl = `
CREATE TABLE IF NOT EXISTS api_keys (
	id TEXT PRIMARY KEY,
	key TEXT UNIQUE NOT NULL,
	name TEXT NOT NULL,
	scopes TEXT NOT NULL,
	created_at DATETIME NOT NULL,
	revoked_at DATETIME NULL,
	expires_at DATETIME NULL,
	rotated_at DATETIME NULL,
	active BOOLEAN NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_api_keys_key ON api_keys(key);`
	}

	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("initialize %s store schema: %w", s.dialect, err)
	}
	if err := s.ensureUsageColumns(); err != nil {
		return err
	}
	return s.prepareStmts()
}

func (s *SQLStore) prepareStmts() error {
	stmts := []struct {
		dest  **sql.Stmt
		query string
	}{
		{&s.stmtGetByID, `SELECT id, key, name, scopes, created_at, revoked_at, expires_at, rotated_at, last_used_at, usage_count, active FROM api_keys WHERE id = ?`},
		{&s.stmtGetByKey, `SELECT id, key, name, scopes, created_at, revoked_at, expires_at, rotated_at, last_used_at, usage_count, active FROM api_keys WHERE key = ?`},
		{&s.stmtRevoke, `UPDATE api_keys SET revoked_at = ?, active = ? WHERE id = ?`},
		{&s.stmtUpdate, `UPDATE api_keys SET name = ?, scopes = ? WHERE id = ?`},
		{&s.stmtSetExpiry, `UPDATE api_keys SET expires_at = ? WHERE id = ?`},
		{&s.stmtDelete, `DELETE FROM api_keys WHERE id = ?`},
		{&s.stmtUsage, `UPDATE api_keys SET usage_count = usage_count + 1, last_used_at = ? WHERE id = ?`},
		{&s.stmtRotate, `UPDATE api_keys SET key = ?, rotated_at = ? WHERE id = ?`},
	}
	for _, s2 := range stmts {
		stmt, err := s.db.Prepare(s.bind(s2.query))
		if err != nil {
			return fmt.Errorf("prepare statement: %w", err)
		}
		*s2.dest = stmt
	}
	return nil
}

func (s *SQLStore) ensureUsageColumns() error {
	alterStatements := []string{
		"ALTER TABLE api_keys ADD COLUMN usage_count INTEGER NOT NULL DEFAULT 0",
	}

	if s.dialect == dialectPostgres {
		alterStatements = append(alterStatements,
			"ALTER TABLE api_keys ADD COLUMN last_used_at TIMESTAMPTZ NULL",
		)
	} else {
		alterStatements = append(alterStatements,
			"ALTER TABLE api_keys ADD COLUMN last_used_at DATETIME NULL",
		)
	}

	for _, stmt := range alterStatements {
		if _, err := s.db.Exec(stmt); err != nil && !isDuplicateColumnError(err) {
			return fmt.Errorf("ensure api_keys usage columns: %w", err)
		}
	}
	return nil
}

// Close closes the underlying SQL connection.
func (s *SQLStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	for _, stmt := range []*sql.Stmt{s.stmtGetByID, s.stmtGetByKey, s.stmtRevoke, s.stmtUpdate, s.stmtSetExpiry, s.stmtDelete, s.stmtUsage, s.stmtRotate} {
		if stmt != nil {
			_ = stmt.Close()
		}
	}
	return s.db.Close()
}

// Create inserts a new API key in the SQL store.
func (s *SQLStore) Create(ctx context.Context, name string, scopes []string, expiresAt *time.Time) (*APIKey, error) {
	if len(scopes) == 0 {
		scopes = []string{ScopeAdmin}
	}
	key, err := generateAPIKeyString()
	if err != nil {
		return nil, err
	}
	id, err := generateID()
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	if expiresAt != nil {
		t := expiresAt.UTC()
		expiresAt = &t
	}

	scopesJSON, err := json.Marshal(scopes)
	if err != nil {
		return nil, fmt.Errorf("encode scopes: %w", err)
	}

	q := s.bind(`
INSERT INTO api_keys(id, key, name, scopes, created_at, revoked_at, expires_at, rotated_at, active, usage_count, last_used_at)
VALUES(?, ?, ?, ?, ?, NULL, ?, NULL, ?, ?, NULL)`)

	//nolint:gosec // G701 false positive: q is a static SQL template (s.bind rewrites placeholders); all values are passed as bound parameters, not interpolated.
	if _, err := s.db.ExecContext(ctx, q, id, key, name, string(scopesJSON), now, expiresAt, true, 0); err != nil {
		return nil, fmt.Errorf("create key: %w", err)
	}

	return &APIKey{
		ID:         id,
		Key:        key,
		Name:       name,
		Scopes:     scopes,
		CreatedAt:  now,
		ExpiresAt:  expiresAt,
		UsageCount: 0,
		Active:     true,
	}, nil
}

// Get retrieves an API key by ID from the SQL store.
func (s *SQLStore) Get(ctx context.Context, id string) (*APIKey, bool) {
	key, err := s.scanOne(ctx, s.stmtGetByID, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false
	}
	if err != nil {
		return nil, false
	}
	return key, true
}

// lookupForMutate fetches a key by ID for lookup-before-mutate paths. Unlike
// Get, it distinguishes a genuine not-found (wrapped ErrKeyNotFound → 404) from
// a transient DB/scan failure (wrapped generic error → 500) so a database
// outage is never reported to callers as a 404.
func (s *SQLStore) lookupForMutate(ctx context.Context, id string) (*APIKey, error) {
	key, err := s.scanOne(ctx, s.stmtGetByID, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrKeyNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("lookup key %s: %w", id, err)
	}
	return key, nil
}

// List returns all API keys with masked key values.
func (s *SQLStore) List(ctx context.Context) []*APIKey {
	q := `
SELECT id, key, name, scopes, created_at, revoked_at, expires_at, rotated_at, last_used_at, usage_count, active
FROM api_keys`

	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return []*APIKey{}
	}
	defer func() {
		_ = rows.Close()
	}()

	keys := make([]*APIKey, 0)
	for rows.Next() {
		k, scanErr := scanAPIKey(rows)
		if scanErr != nil {
			continue
		}
		masked := *k
		masked.Key = maskKey(masked.Key)
		keys = append(keys, &masked)
	}
	return keys
}

// Revoke marks an API key as inactive and records the revocation timestamp.
func (s *SQLStore) Revoke(ctx context.Context, id string) error {
	now := time.Now().UTC()
	res, err := s.stmtRevoke.ExecContext(ctx, now, false, id)
	if err != nil {
		return fmt.Errorf("revoke key: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("%w: %s", ErrKeyNotFound, id)
	}
	return nil
}

// Update modifies API key metadata (name/scopes).
func (s *SQLStore) Update(ctx context.Context, id string, name string, scopes []string) (*APIKey, error) {
	current, err := s.lookupForMutate(ctx, id)
	if err != nil {
		return nil, err
	}

	if name != "" {
		current.Name = name
	}
	if len(scopes) > 0 {
		current.Scopes = scopes
	}

	scopesJSON, err := json.Marshal(current.Scopes)
	if err != nil {
		return nil, fmt.Errorf("encode scopes: %w", err)
	}

	if _, err := s.stmtUpdate.ExecContext(ctx, current.Name, string(scopesJSON), id); err != nil {
		return nil, fmt.Errorf("update key: %w", err)
	}

	masked := *current
	masked.Key = maskKey(masked.Key)
	return &masked, nil
}

// SetExpiration updates or clears the API key expiration time.
func (s *SQLStore) SetExpiration(ctx context.Context, id string, expiresAt *time.Time) error {
	if expiresAt != nil {
		t := expiresAt.UTC()
		expiresAt = &t
	}

	res, err := s.stmtSetExpiry.ExecContext(ctx, expiresAt, id)
	if err != nil {
		return fmt.Errorf("set key expiration: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("%w: %s", ErrKeyNotFound, id)
	}
	return nil
}

// Delete removes an API key by ID.
func (s *SQLStore) Delete(ctx context.Context, id string) error {
	res, err := s.stmtDelete.ExecContext(ctx, id)
	if err != nil {
		return fmt.Errorf("delete key: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("%w: %s", ErrKeyNotFound, id)
	}
	return nil
}

// ValidateKey validates a full API key value and updates usage counters.
// Auth succeeds as long as the key lookup and validity checks pass.
// A transient failure of the usage-counter UPDATE is logged but does not fail
// authentication — dropping one increment is preferable to returning a 401 on a
// legitimate request.
func (s *SQLStore) ValidateKey(ctx context.Context, key string) (*APIKey, bool) {
	apiKey, err := s.scanOne(ctx, s.stmtGetByKey, key)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false
	}
	if err != nil {
		return nil, false
	}
	if !apiKey.Active || apiKey.RevokedAt != nil {
		return nil, false
	}
	if apiKey.ExpiresAt != nil && time.Now().After(*apiKey.ExpiresAt) {
		return nil, false
	}

	// Auth check passed. Attempt to update usage counters. A failure here is
	// non-fatal: log the error and return the authenticated key anyway.
	now := time.Now().UTC()
	if _, counterErr := s.stmtUsage.ExecContext(ctx, now, apiKey.ID); counterErr != nil {
		slog.Warn("failed to update key usage counter; authentication still succeeds",
			"key_id", apiKey.ID, "error", counterErr)
	} else {
		apiKey.UsageCount++
		apiKey.LastUsedAt = &now
	}
	return apiKey, true
}

// RotateKey rotates the secret value for an existing API key.
func (s *SQLStore) RotateKey(ctx context.Context, id string) (*APIKey, error) {
	newKey, err := generateAPIKeyString()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()

	res, err := s.stmtRotate.ExecContext(ctx, newKey, now, id)
	if err != nil {
		return nil, fmt.Errorf("rotate key: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return nil, fmt.Errorf("%w: %s", ErrKeyNotFound, id)
	}

	updated, err := s.lookupForMutate(ctx, id)
	if err != nil {
		return nil, err
	}
	return updated, nil
}

func (s *SQLStore) scanOne(ctx context.Context, stmt *sql.Stmt, arg any) (*APIKey, error) {
	return scanAPIKey(stmt.QueryRowContext(ctx, arg))
}

func scanAPIKey(scanner interface {
	Scan(dest ...any) error
}) (*APIKey, error) {
	var (
		k         APIKey
		scopesRaw string
		revoked   sql.NullTime
		expires   sql.NullTime
		rotated   sql.NullTime
		lastUsed  sql.NullTime
	)

	err := scanner.Scan(
		&k.ID,
		&k.Key,
		&k.Name,
		&scopesRaw,
		&k.CreatedAt,
		&revoked,
		&expires,
		&rotated,
		&lastUsed,
		&k.UsageCount,
		&k.Active,
	)
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal([]byte(scopesRaw), &k.Scopes); err != nil {
		return nil, fmt.Errorf("decode scopes: %w", err)
	}
	if revoked.Valid {
		t := revoked.Time
		k.RevokedAt = &t
	}
	if expires.Valid {
		t := expires.Time
		k.ExpiresAt = &t
	}
	if rotated.Valid {
		t := rotated.Time
		k.RotatedAt = &t
	}
	if lastUsed.Valid {
		t := lastUsed.Time
		k.LastUsedAt = &t
	}
	return &k, nil
}

func isDuplicateColumnError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate column") ||
		strings.Contains(msg, "already exists")
}

func (s *SQLStore) bind(query string) string {
	if s.dialect != dialectPostgres {
		return query
	}
	var (
		b      strings.Builder
		argNum = 1
	)
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			fmt.Fprintf(&b, "$%d", argNum)
			argNum++
			continue
		}
		b.WriteByte(query[i])
	}
	return b.String()
}

func generateAPIKeyString() (string, error) {
	keyBytes := make([]byte, 32)
	if _, err := rand.Read(keyBytes); err != nil {
		return "", fmt.Errorf("generating key: %w", err)
	}
	return "fgw_" + hex.EncodeToString(keyBytes), nil
}

func generateID() (string, error) {
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return "", fmt.Errorf("generating id: %w", err)
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x",
		idBytes[0:4], idBytes[4:6], idBytes[6:8], idBytes[8:10], idBytes[10:16]), nil
}
