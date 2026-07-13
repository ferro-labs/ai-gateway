package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/internal/migrations"
	"github.com/ferro-labs/ai-gateway/internal/sqldb"
)

// ConfigStore persists the gateway config for runtime management APIs.
//
// Every method accepts a context.Context as its first parameter so request
// cancellation and deadlines propagate down to the underlying storage layer.
type ConfigStore interface {
	Save(ctx context.Context, cfg aigateway.Config) error
	Load(ctx context.Context) (aigateway.Config, bool, error)
	Delete(ctx context.Context) error
}

// ConfigResetter provides reset semantics for config CRUD APIs.
type ConfigResetter interface {
	ResetConfig(ctx context.Context) error
}

var (
	errConfigValidation  = errors.New("config validation failed")
	errConfigPersistence = errors.New("config persistence failed")
)

// SQLConfigStore persists config snapshots in SQLite/Postgres.
type SQLConfigStore struct {
	db      *sql.DB
	dialect sqldb.Dialect
}

// PersistedConfigVersion is one durable config_history record: a config snapshot
// and the version and time at which it became the active config.
type PersistedConfigVersion struct {
	Version   int
	Config    aigateway.Config
	UpdatedAt time.Time
}

// NewSQLiteConfigStore creates a SQLite-backed config store.
func NewSQLiteConfigStore(ctx context.Context, dsn string) (*SQLConfigStore, error) {
	db, err := sqldb.Open(ctx, sqldb.SQLite, dsn, "ferrogw-config.db")
	if err != nil {
		return nil, err
	}
	s := &SQLConfigStore{db: db, dialect: sqldb.SQLite}
	if err := s.init(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// NewPostgresConfigStore creates a Postgres-backed config store.
func NewPostgresConfigStore(ctx context.Context, dsn string) (*SQLConfigStore, error) {
	db, err := sqldb.Open(ctx, sqldb.Postgres, dsn, "")
	if err != nil {
		return nil, err
	}
	s := &SQLConfigStore{db: db, dialect: sqldb.Postgres}
	if err := s.init(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *SQLConfigStore) init(ctx context.Context) error {
	if err := migrations.RunNamed(ctx, s.db, s.dialect, configLedger, "gateway_config", configStoreSteps(s.dialect)); err != nil {
		return fmt.Errorf("migrate %s config schema: %w", s.dialect, err)
	}
	return nil
}

// Save persists the current gateway config snapshot and appends it to the
// config_history audit trail in a single transaction. Writing both together
// guarantees the persisted active config and its audit trail can never diverge:
// a crash either leaves both updated or neither, never one without the other.
func (s *SQLConfigStore) Save(ctx context.Context, cfg aigateway.Config) error {
	data, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin config save: %w", err)
	}
	// Rollback is a no-op once Commit succeeds; it undoes the upsert if any
	// later statement in this transaction fails.
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC()

	// A single upsert serves both dialects: 'excluded' resolves case-insensitively
	// on Postgres and SQLite alike, so only the placeholders differ.
	upsert := sqldb.Bind(s.dialect, `
INSERT INTO gateway_config(id, config_json, updated_at)
VALUES(1, ?, ?)
ON CONFLICT(id) DO UPDATE SET config_json = excluded.config_json, updated_at = excluded.updated_at`)
	if _, err := tx.ExecContext(ctx, upsert, string(data), now); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	var nextVersion int
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) + 1 FROM config_history").Scan(&nextVersion); err != nil {
		return fmt.Errorf("next config history version: %w", err)
	}
	histInsert := sqldb.Bind(s.dialect, "INSERT INTO config_history(version, config_json, updated_at) VALUES(?, ?, ?)")
	if _, err := tx.ExecContext(ctx, histInsert, nextVersion, string(data), now); err != nil {
		return fmt.Errorf("append config history: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit config save: %w", err)
	}
	return nil
}

// LoadHistory returns the persisted config audit trail in version order. Each
// record is a snapshot that was active at that version; Save writes these
// atomically with the active config, so the trail never diverges from what was
// persisted as active.
func (s *SQLConfigStore) LoadHistory(ctx context.Context) ([]PersistedConfigVersion, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT version, config_json, updated_at FROM config_history ORDER BY version")
	if err != nil {
		return nil, fmt.Errorf("load config history: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var history []PersistedConfigVersion
	for rows.Next() {
		var (
			rec PersistedConfigVersion
			raw string
		)
		if err := rows.Scan(&rec.Version, &raw, &rec.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan config history row: %w", err)
		}
		if err := json.Unmarshal([]byte(raw), &rec.Config); err != nil {
			return nil, fmt.Errorf("decode config history: %w", err)
		}
		history = append(history, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate config history: %w", err)
	}
	return history, nil
}

// Load returns the persisted config snapshot when one exists.
func (s *SQLConfigStore) Load(ctx context.Context) (aigateway.Config, bool, error) {
	query := `SELECT config_json FROM gateway_config WHERE id = 1`
	row := s.db.QueryRowContext(ctx, query)
	var raw string
	if err := row.Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return aigateway.Config{}, false, nil
		}
		return aigateway.Config{}, false, fmt.Errorf("load config: %w", err)
	}

	var cfg aigateway.Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return aigateway.Config{}, false, fmt.Errorf("decode config: %w", err)
	}
	return cfg, true, nil
}

// Delete removes the persisted config snapshot.
func (s *SQLConfigStore) Delete(ctx context.Context) error {
	query := `DELETE FROM gateway_config WHERE id = 1`
	if _, err := s.db.ExecContext(ctx, query); err != nil {
		return fmt.Errorf("delete config: %w", err)
	}
	return nil
}

// Ping verifies the backing database is reachable.
func (s *SQLConfigStore) Ping(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("config store ping: store not initialized")
	}
	if err := s.db.PingContext(ctx); err != nil {
		return fmt.Errorf("config store ping: %w", err)
	}
	return nil
}

// Close closes the underlying SQL connection.
func (s *SQLConfigStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// GatewayConfigManager connects runtime gateway config operations to optional
// persistent storage.
type GatewayConfigManager struct {
	mu      sync.RWMutex
	gw      *aigateway.Gateway
	initial aigateway.Config
	store   ConfigStore
}

// NewGatewayConfigManager creates a config manager backed by an optional persistent store.
func NewGatewayConfigManager(gw *aigateway.Gateway, store ConfigStore) (*GatewayConfigManager, error) {
	if gw == nil {
		return nil, fmt.Errorf("gateway is required")
	}

	m := &GatewayConfigManager{
		gw:      gw,
		initial: gw.GetConfig(),
		store:   store,
	}

	if store != nil {
		// Startup-scoped load: there is no request context at construction time,
		// so a background context is the correct choice here.
		persisted, ok, err := store.Load(context.Background())
		if err != nil {
			return nil, err
		}
		if ok {
			if err := gw.ReloadConfig(context.Background(), persisted); err != nil {
				return nil, fmt.Errorf("reload persisted config: %w", err)
			}
		}
	}

	return m, nil
}

// GetConfig returns the active runtime config.
func (m *GatewayConfigManager) GetConfig() aigateway.Config {
	return m.gw.GetConfig()
}

// Ping reports whether the config manager's backing store is reachable. A
// manager with no persistent store keeps config in memory and is always
// reachable, so it returns nil. A nil manager is not initialized and must
// fail closed rather than dereference m.store.
func (m *GatewayConfigManager) Ping(ctx context.Context) error {
	if m == nil {
		return fmt.Errorf("config manager ping: manager not initialized")
	}
	if m.store == nil {
		return nil
	}
	if p, ok := m.store.(interface{ Ping(context.Context) error }); ok {
		return p.Ping(ctx)
	}
	return nil
}

// ReloadConfig validates/applies config and persists it when a store is configured.
func (m *GatewayConfigManager) ReloadConfig(ctx context.Context, cfg aigateway.Config) error {
	if err := aigateway.ValidateConfig(cfg); err != nil {
		return errors.Join(errConfigValidation, err)
	}

	previousCfg := m.gw.GetConfig()

	if m.store != nil {
		if err := m.store.Save(ctx, cfg); err != nil {
			return errors.Join(errConfigPersistence, err)
		}
	}

	if err := m.gw.ReloadConfig(ctx, cfg); err != nil {
		if m.store != nil {
			// Save happened before apply. If apply fails, attempt to restore
			// persisted state so runtime and storage remain aligned.
			if rollbackErr := m.store.Save(ctx, previousCfg); rollbackErr != nil {
				return errors.Join(
					errConfigValidation,
					errConfigPersistence,
					fmt.Errorf("apply config failed: %w", err),
					fmt.Errorf("rollback persisted config failed: %w", rollbackErr),
				)
			}
		}
		return errors.Join(errConfigValidation, err)
	}

	return nil
}

// ResetConfig restores startup config and clears persisted overrides.
func (m *GatewayConfigManager) ResetConfig(ctx context.Context) error {
	m.mu.RLock()
	initial := m.initial
	m.mu.RUnlock()

	if err := m.gw.ReloadConfig(ctx, initial); err != nil {
		return err
	}
	if m.store != nil {
		if err := m.store.Delete(ctx); err != nil {
			return err
		}
	}
	return nil
}

// Close closes any underlying persistent config store.
func (m *GatewayConfigManager) Close() error {
	if m == nil || m.store == nil {
		return nil
	}
	closer, ok := m.store.(interface{ Close() error })
	if !ok {
		return nil
	}
	return closer.Close()
}
