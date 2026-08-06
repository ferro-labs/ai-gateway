package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/internal/admin/model"
	"github.com/ferro-labs/ai-gateway/internal/migrations"
	"github.com/ferro-labs/ai-gateway/internal/sqldb"
	"github.com/ferro-labs/ai-gateway/pkg/logger"
)

// ConfigStore persists the gateway config for runtime management APIs.
//
// Every method accepts a context.Context as its first parameter so request
// cancellation and deadlines propagate down to the underlying storage layer.
type ConfigStore interface {
	Save(ctx context.Context, cfg config.Config) error
	Load(ctx context.Context) (config.Config, bool, error)
	Delete(ctx context.Context) error
}

// ConfigResetter provides reset semantics for config CRUD APIs.
type ConfigResetter interface {
	ResetConfig(ctx context.Context) error
}

// ConfigHistoryLoader exposes the durable config-version trail to the config
// CRUD APIs, so what they serve and roll back to is the record that survives a
// restart rather than one rebuilt in memory since boot.
type ConfigHistoryLoader interface {
	LoadHistory(ctx context.Context) ([]model.PersistedConfigVersion, bool, error)
}

var errConfigValidation = errors.New("config validation failed")

// ErrConfigPersistence wraps a failure to persist an already-validated config to
// the store. Handlers use errors.Is to distinguish it (reported as 500) from a
// validation failure (reported as 400).
var ErrConfigPersistence = errors.New("config persistence failed")

// maxConfigHistoryEntries caps the number of config_history rows LoadHistory
// decodes into memory for one call. It matches the handlers' in-memory history
// bound of the same name; the two are kept equal by intent but declared
// separately so this package does not depend on handlers.
const maxConfigHistoryEntries = 200

// SQLConfigStore persists config snapshots in SQLite/Postgres.
type SQLConfigStore struct {
	db      *sql.DB
	dialect sqldb.Dialect
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
func (s *SQLConfigStore) Save(ctx context.Context, cfg config.Config) error {
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

	if err := s.appendHistory(ctx, tx, string(data), now); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit config save: %w", err)
	}
	return nil
}

// rollbackFromKey is the context slot naming the version a rollback replaced.
type rollbackFromKey struct{}

// WithRollbackFrom marks ctx as applying a rollback that supersedes version, so
// the config_history row the write produces records that provenance instead of
// reading as an ordinary update.
//
// It rides in the context for the same reason the actor does: the handler that
// knows the fact and the store that records it are separated by the ConfigStore
// interface, and threading a parameter through would change a signature every
// implementation shares to carry something one caller ever sets.
func WithRollbackFrom(ctx context.Context, version int) context.Context {
	return context.WithValue(ctx, rollbackFromKey{}, version)
}

// rollbackFrom returns the superseded version, or nil when this write is not a
// rollback. The nil is what binds as SQL NULL — the column's "ordinary update".
func rollbackFrom(ctx context.Context) any {
	if version, ok := ctx.Value(rollbackFromKey{}).(int); ok {
		return version
	}
	return nil
}

// appendHistory writes one config_history row at the next version inside the
// caller's transaction, so the trail commits with whatever change produced it
// and shares that change's timestamp.
func (s *SQLConfigStore) appendHistory(ctx context.Context, tx *sql.Tx, configJSON string, now time.Time) error {
	var nextVersion int
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) + 1 FROM config_history").Scan(&nextVersion); err != nil {
		return fmt.Errorf("next config history version: %w", err)
	}
	insert := sqldb.Bind(s.dialect, "INSERT INTO config_history(version, config_json, updated_at, actor, rolled_back_from) VALUES(?, ?, ?, ?, ?)")
	if _, err := tx.ExecContext(ctx, insert, nextVersion, configJSON, now, model.ActorFromContext(ctx), rollbackFrom(ctx)); err != nil {
		return fmt.Errorf("append config history: %w", err)
	}
	return nil
}

// LoadHistory returns the most recent persisted config versions in ascending
// version order. Each record is a snapshot that was active at that version;
// Save writes these atomically with the active config, so the trail never
// diverges from what was persisted as active.
//
// The read is capped at maxConfigHistoryEntries — matching the in-memory
// history bound — so a gateway with a long audit trail never decodes every
// stored snapshot into memory to answer one call. The cap is a read bound
// only: no history row is ever deleted, and older versions stay queryable.
func (s *SQLConfigStore) LoadHistory(ctx context.Context) ([]model.PersistedConfigVersion, error) {
	query := sqldb.Bind(s.dialect, "SELECT version, config_json, updated_at, actor, rolled_back_from FROM config_history ORDER BY version DESC LIMIT ?")
	rows, err := s.db.QueryContext(ctx, query, maxConfigHistoryEntries)
	if err != nil {
		return nil, fmt.Errorf("load config history: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var history []model.PersistedConfigVersion
	for rows.Next() {
		var (
			rec            model.PersistedConfigVersion
			raw            string
			rolledBackFrom sql.NullInt64
		)
		if err := rows.Scan(&rec.Version, &raw, &rec.UpdatedAt, &rec.Actor, &rolledBackFrom); err != nil {
			return nil, fmt.Errorf("scan config history row: %w", err)
		}
		if rolledBackFrom.Valid {
			version := int(rolledBackFrom.Int64)
			rec.RolledBackFrom = &version
		}
		if err := json.Unmarshal([]byte(raw), &rec.Config); err != nil {
			return nil, fmt.Errorf("decode config history: %w", err)
		}
		history = append(history, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate config history: %w", err)
	}
	// Rows arrive newest-first so the LIMIT selects the newest versions; flip
	// back to ascending order, which is what callers expect.
	slices.Reverse(history)
	return history, nil
}

// Load returns the persisted config snapshot when one exists.
func (s *SQLConfigStore) Load(ctx context.Context) (config.Config, bool, error) {
	query := `SELECT config_json FROM gateway_config WHERE id = 1`
	row := s.db.QueryRowContext(ctx, query)
	var raw string
	if err := row.Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return config.Config{}, false, nil
		}
		return config.Config{}, false, fmt.Errorf("load config: %w", err)
	}

	var cfg config.Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return config.Config{}, false, fmt.Errorf("decode config: %w", err)
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

// DeleteAndRecord removes the persisted config snapshot and appends active — the
// config the gateway runs once the override is gone — to the trail in the same
// transaction. A plain Delete would leave the newest recorded version naming the
// config the reset just replaced, which is exactly the divergence between the
// trail and the running config the trail exists to detect.
func (s *SQLConfigStore) DeleteAndRecord(ctx context.Context, active config.Config) error {
	data, err := json.Marshal(active)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin config delete: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM gateway_config WHERE id = 1`); err != nil {
		return fmt.Errorf("delete config: %w", err)
	}
	if err := s.appendHistory(ctx, tx, string(data), time.Now().UTC()); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit config delete: %w", err)
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
	// mu serializes config mutations so persisting a config and applying it to
	// the gateway are a single step. Two concurrent writers could otherwise
	// persist one config while leaving the other active — a divergence that
	// survives a restart, because the next boot adopts whatever was persisted.
	//
	// The lock belongs here rather than only in the admin handlers: this type
	// owns both the store and the gateway, and it is the only place that knows
	// the two must move together. Callers get the invariant for free.
	mu      sync.Mutex
	gw      *aigateway.Gateway
	initial config.Config
	store   ConfigStore
	// adoptedStored records that a persisted config was found at construction
	// and now supersedes whatever the gateway was built from. Written once,
	// before the manager is returned, and read-only afterwards.
	adoptedStored bool
	// diverged records that the running config and the persisted one describe
	// different states and this process can no longer reconcile them. See
	// Ping, which is what acts on it. Atomic rather than guarded by mu: Ping is
	// on the readiness path and must not queue behind a config apply.
	diverged atomic.Bool
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
			m.adoptedStored = true
		}
	}

	return m, nil
}

// AdoptedStoredConfig reports whether the config the gateway is running came
// from this manager's persistent store rather than from whatever the gateway
// was constructed with.
//
// The store winning is correct — it is the newest operator intent and the thing
// a rollback restores — but it happens after the startup log has already
// described the file, so the caller needs to be able to say which source is
// actually live. Reporting it is this type's job because it is the only one
// that knows.
func (m *GatewayConfigManager) AdoptedStoredConfig() bool {
	return m != nil && m.adoptedStored
}

// GetConfig returns the active runtime config.
func (m *GatewayConfigManager) GetConfig() config.Config {
	return m.gw.GetConfig()
}

// Ping reports whether the config manager's backing store is reachable. A
// manager with no persistent store keeps config in memory and is always
// reachable, so it returns nil. A nil manager is not initialized and must
// fail closed rather than dereference m.store.
//
// It also fails once ReloadConfig has recorded a divergence it could not undo,
// which is not a reachability question but is answered here because readiness
// is what has to change. An instance running a config its store disagrees with
// serves traffic under settings that vanish on the next restart — the store
// wins on boot — so it must leave rotation, and the flag never clears because
// nothing short of a restart resolves which config is real. Exiting instead
// would turn one failed write into a crash loop.
func (m *GatewayConfigManager) Ping(ctx context.Context) error {
	if m == nil {
		return fmt.Errorf("config manager ping: manager not initialized")
	}
	if m.diverged.Load() {
		return fmt.Errorf("config manager ping: the running config could not be reverted after a failed persist and no longer matches the store; restart to adopt the persisted config")
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
func (m *GatewayConfigManager) ReloadConfig(ctx context.Context, cfg config.Config) error {
	if err := config.ValidateConfig(cfg); err != nil {
		return errors.Join(errConfigValidation, err)
	}

	// Validation is pure, so it stays outside the lock; everything that touches
	// the store or the gateway is inside it.
	m.mu.Lock()
	defer m.mu.Unlock()

	previousCfg := m.gw.GetConfig()

	// Apply before persisting: only a config the gateway has actually accepted
	// is allowed to reach the store. Persisting first leaves a config the
	// gateway rejected sitting in the store, where the next boot adopts it.
	if err := m.gw.ReloadConfig(ctx, cfg); err != nil {
		return errors.Join(errConfigValidation, err)
	}

	if m.store != nil {
		warnStoredLiterals(cfg)
		if err := m.store.Save(ctx, cfg); err != nil {
			// The gateway is running cfg but the store never took it, so a
			// restart would come back on previousCfg. Put the gateway back on
			// that one rather than leave the two describing different states.
			if revertErr := m.gw.ReloadConfig(ctx, previousCfg); revertErr != nil {
				// Both halves failed, so there is no config this process can
				// put itself back on: it is running cfg, the store holds
				// previousCfg, and the next boot adopts the store's. Nothing
				// here can decide which is right, so the instance stops
				// claiming it can serve and an operator resolves it. See Ping.
				m.diverged.Store(true)
				logger.Default().Error(
					"config persist and revert both failed; the running config no longer matches the store and this instance is now reporting not ready",
					"persist_error", err,
					"revert_error", revertErr,
				)
				return errors.Join(
					ErrConfigPersistence,
					err,
					fmt.Errorf("revert applied config failed: %w", revertErr),
				)
			}
			return errors.Join(ErrConfigPersistence, err)
		}
	}

	warnBootOnlyChanges(previousCfg, cfg)

	return nil
}

// warnStoredLiterals reports a config value that is about to be written to the
// store as written while GET /admin/config withholds it.
//
// The two paths disagree today: Save persists json.Marshal(cfg) verbatim, and
// only the read path scrubs. An operator who inlines a credential — rather than
// naming it with the documented ${VAR}, which stores the reference and never the
// value — therefore lands plaintext in gateway_config and config_history while
// the API answers "[REDACTED]" for the same field, which reads as protection it
// does not have.
//
// It warns rather than scrubbing or rejecting, and both alternatives are worse.
// Scrubbing on the way in would persist the placeholder, so the stored config
// could not be restored or rolled back to — the store would hold a config that
// cannot run. Rejecting would refuse configs that work today. A warning names
// the field and leaves the decision where it belongs.
//
// The first such field is named rather than all of them: the message exists to
// start an edit, not to inventory one, and the next apply names the next.
//
// What counts as "withheld" is decided by running the config through the very
// scrubber the read path uses and asking which field it changed, so the warning
// cannot drift from what GET /admin/config actually serves. That set is wider
// than "credentials": a free-form map the gateway hands to something else is
// withheld key by key whatever it holds, so a plain setting in one warns too.
// That is the honest boundary — the gateway cannot read those schemas, which is
// exactly why the value is withheld — and the message says stored-and-withheld
// rather than accusing the value of being a secret.
func warnStoredLiterals(cfg config.Config) {
	field := RedactedLiteralField(ScrubConfigSecrets(cfg))
	if field == "" {
		return
	}
	logger.Default().Warn(
		"config value is stored as written but withheld from GET /admin/config; if it is a credential, replace it with a ${VAR} reference so only the reference is persisted",
		"field", field,
	)
}

// warnBootOnlyChanges reports a config section that this process reads once at
// startup and therefore cannot adopt without a restart.
//
// The change is still accepted and persisted rather than rejected: it is valid
// config, a rollback to an older version must be able to carry it, and the next
// start will apply it. What must not happen is the silence — GET /admin/config
// reports the new value the moment it is stored, so with no signal the operator
// is told a setting took effect that did not. PostgreSQL treats a
// restart-scoped setting the same way: ALTER SYSTEM records it, pg_settings
// reports it, and pending_restart marks it as not yet live.
//
// Observability is the only such section today. Everything else in Config is
// re-read on reload: strategy, targets, plugins, aliases, mcp_servers,
// request_timeout, compatibility and max_request_bytes all take effect
// immediately. internal/otel.Init runs once during startup, so tracing
// endpoint, sampler, privacy level and exporters keep the values they were
// built with.
//
// The log line is the weak form of this signal — a durable flag on
// GET /admin/config beside the value, as pg_settings.pending_restart is, is
// what an operator should be able to query. That belongs to the admin handler.
func warnBootOnlyChanges(previous, next config.Config) {
	if reflect.DeepEqual(previous.Observability, next.Observability) {
		return
	}
	logger.Default().Warn(
		"observability config accepted and persisted but not applied: it is read once at startup and takes effect on the next restart",
		"section", "observability",
		"pending_restart", true,
	)
}

// LoadHistory returns the durable config-version trail. The bool reports whether
// a trail is kept at all, which is not the same answer as an empty one: a
// manager with no persistent store has no durable versions to serve, and the
// caller falls back to its own in-memory record instead of serving nothing.
func (m *GatewayConfigManager) LoadHistory(ctx context.Context) ([]model.PersistedConfigVersion, bool, error) {
	if m == nil || m.store == nil {
		return nil, false, nil
	}
	loader, ok := m.store.(interface {
		LoadHistory(context.Context) ([]model.PersistedConfigVersion, error)
	})
	if !ok {
		return nil, false, nil
	}
	history, err := loader.LoadHistory(ctx)
	if err != nil {
		return nil, true, err
	}
	return history, true, nil
}

// ResetConfig restores startup config and clears persisted overrides.
func (m *GatewayConfigManager) ResetConfig(ctx context.Context) error {
	// Reset is a mutation too: it must not interleave with a ReloadConfig, or
	// the runtime config and the persisted one end up describing different
	// states. initial is written once at construction and never mutated, so the
	// lock is here for the ordering, not for the read.
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.gw.ReloadConfig(ctx, m.initial); err != nil {
		return err
	}
	if m.store != nil {
		if err := m.clearStored(ctx); err != nil {
			// The apply already succeeded, so the gateway is running the
			// startup config while the store still holds the override it
			// replaced — and a restart would load that override back. Record
			// what is actually running instead, so the two agree even though
			// clearing the override failed.
			if saveErr := m.store.Save(ctx, m.initial); saveErr != nil {
				return errors.Join(ErrConfigPersistence, err, saveErr)
			}
			return errors.Join(ErrConfigPersistence, err)
		}
	}
	return nil
}

// clearStored removes the persisted override and, when the store can do both in
// one transaction, records the startup config that takes over as the newest
// version. Without that record the trail's newest version keeps naming the
// override the reset just discarded.
func (m *GatewayConfigManager) clearStored(ctx context.Context) error {
	if d, ok := m.store.(interface {
		DeleteAndRecord(context.Context, config.Config) error
	}); ok {
		return d.DeleteAndRecord(ctx, m.initial)
	}
	return m.store.Delete(ctx)
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
