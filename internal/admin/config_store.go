package admin

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	aigateway "github.com/ferro-labs/ai-gateway"
	_ "github.com/lib/pq"
	_ "modernc.org/sqlite"
)

// ConfigStore persists the gateway config for runtime management APIs.
type ConfigStore interface {
	Save(cfg aigateway.Config) error
	Load() (aigateway.Config, bool, error)
	Delete() error
}

// ConfigResetter provides reset semantics for config CRUD APIs.
type ConfigResetter interface {
	ResetConfig() error
}

type sqlConfigDialect string

const (
	configDialectSQLite   sqlConfigDialect = "sqlite"
	configDialectPostgres sqlConfigDialect = "postgres"
)

// SQLConfigStore persists config snapshots in SQLite/Postgres.
type SQLConfigStore struct {
	db      *sql.DB
	dialect sqlConfigDialect
}

func NewSQLiteConfigStore(dsn string) (*SQLConfigStore, error) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		dsn = "ferrogw-config.db"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite config store: %w", err)
	}
	s := &SQLConfigStore{db: db, dialect: configDialectSQLite}
	if err := s.init(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func NewPostgresConfigStore(dsn string) (*SQLConfigStore, error) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return nil, fmt.Errorf("postgres dsn is required")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres config store: %w", err)
	}
	s := &SQLConfigStore{db: db, dialect: configDialectPostgres}
	if err := s.init(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *SQLConfigStore) init() error {
	if err := s.db.Ping(); err != nil {
		return fmt.Errorf("ping %s config store: %w", s.dialect, err)
	}

	ddl := `
CREATE TABLE IF NOT EXISTS gateway_config (
	id INTEGER PRIMARY KEY,
	config_json TEXT NOT NULL,
	updated_at TIMESTAMP NOT NULL
);`

	if s.dialect == configDialectPostgres {
		ddl = `
CREATE TABLE IF NOT EXISTS gateway_config (
	id SMALLINT PRIMARY KEY,
	config_json TEXT NOT NULL,
	updated_at TIMESTAMPTZ NOT NULL
);`
	}

	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("initialize config schema: %w", err)
	}
	return nil
}

func (s *SQLConfigStore) Save(cfg aigateway.Config) error {
	data, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	upsert := `
INSERT INTO gateway_config(id, config_json, updated_at)
VALUES(1, ?, ?)
ON CONFLICT(id) DO UPDATE SET config_json = excluded.config_json, updated_at = excluded.updated_at`

	if s.dialect == configDialectPostgres {
		upsert = `
INSERT INTO gateway_config(id, config_json, updated_at)
VALUES(1, $1, $2)
ON CONFLICT(id) DO UPDATE SET config_json = EXCLUDED.config_json, updated_at = EXCLUDED.updated_at`
	}

	if _, err := s.db.Exec(upsert, string(data), time.Now().UTC()); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	return nil
}

func (s *SQLConfigStore) Load() (aigateway.Config, bool, error) {
	query := `SELECT config_json FROM gateway_config WHERE id = 1`
	row := s.db.QueryRow(query)
	var raw string
	if err := row.Scan(&raw); err != nil {
		if err == sql.ErrNoRows {
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

func (s *SQLConfigStore) Delete() error {
	query := `DELETE FROM gateway_config WHERE id = 1`
	if _, err := s.db.Exec(query); err != nil {
		return fmt.Errorf("delete config: %w", err)
	}
	return nil
}

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
		persisted, ok, err := store.Load()
		if err != nil {
			return nil, err
		}
		if ok {
			if err := gw.ReloadConfig(persisted); err != nil {
				return nil, fmt.Errorf("reload persisted config: %w", err)
			}
		}
	}

	return m, nil
}

func (m *GatewayConfigManager) GetConfig() aigateway.Config {
	return m.gw.GetConfig()
}

func (m *GatewayConfigManager) ReloadConfig(cfg aigateway.Config) error {
	if err := m.gw.ReloadConfig(cfg); err != nil {
		return err
	}
	if m.store != nil {
		if err := m.store.Save(cfg); err != nil {
			return err
		}
	}
	return nil
}

func (m *GatewayConfigManager) ResetConfig() error {
	m.mu.RLock()
	initial := m.initial
	m.mu.RUnlock()

	if err := m.gw.ReloadConfig(initial); err != nil {
		return err
	}
	if m.store != nil {
		if err := m.store.Delete(); err != nil {
			return err
		}
	}
	return nil
}
