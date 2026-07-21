package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	aigateway "github.com/ferro-labs/ai-gateway"
)

func sqliteObjectExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var got string
	err := db.QueryRowContext(context.Background(),
		"SELECT name FROM sqlite_master WHERE name = ?", name).Scan(&got)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		t.Fatalf("probe sqlite object %q: %v", name, err)
	}
	return true
}

func fallbackConfig() aigateway.Config {
	return aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeFallback},
		Targets:  []aigateway.Target{{VirtualKey: "openai"}, {VirtualKey: "anthropic"}},
	}
}

func singleConfig() aigateway.Config {
	return aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
		Targets:  []aigateway.Target{{VirtualKey: "openai"}},
	}
}

// TestSQLConfigStore_SaveWritesConfigAndHistoryAtomically proves both the active
// config and its audit trail commit together: after two saves the active config
// is the latest and config_history holds one versioned row per save.
func TestSQLConfigStore_SaveWritesConfigAndHistoryAtomically(t *testing.T) {
	store, err := NewSQLiteConfigStore(t.Context(), filepath.Join(t.TempDir(), "config.db"))
	if err != nil {
		t.Fatalf("new config store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	first, second := singleConfig(), fallbackConfig()
	if err := store.Save(context.Background(), first); err != nil {
		t.Fatalf("save first: %v", err)
	}
	if err := store.Save(context.Background(), second); err != nil {
		t.Fatalf("save second: %v", err)
	}

	loaded, ok, err := store.Load(context.Background())
	if err != nil || !ok {
		t.Fatalf("load: ok=%v err=%v", ok, err)
	}
	if loaded.Strategy.Mode != second.Strategy.Mode {
		t.Fatalf("active config mode = %q, want %q", loaded.Strategy.Mode, second.Strategy.Mode)
	}

	history, err := store.LoadHistory(context.Background())
	if err != nil {
		t.Fatalf("load history: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("history len = %d, want 2", len(history))
	}
	if history[0].Version != 1 || history[1].Version != 2 {
		t.Fatalf("history versions = %d,%d, want 1,2", history[0].Version, history[1].Version)
	}
	if history[0].Config.Strategy.Mode != first.Strategy.Mode {
		t.Fatalf("history[0] mode = %q, want %q", history[0].Config.Strategy.Mode, first.Strategy.Mode)
	}
	if history[1].Config.Strategy.Mode != second.Strategy.Mode {
		t.Fatalf("history[1] mode = %q, want %q", history[1].Config.Strategy.Mode, second.Strategy.Mode)
	}
}

// TestSQLConfigStore_SaveRollsBackConfigWhenHistoryFails proves the write is
// atomic in the other direction: when the history portion of the transaction
// fails, the active-config upsert is rolled back with it, so the persisted
// active config never advances without a matching history record.
func TestSQLConfigStore_SaveRollsBackConfigWhenHistoryFails(t *testing.T) {
	store, err := NewSQLiteConfigStore(t.Context(), filepath.Join(t.TempDir(), "config.db"))
	if err != nil {
		t.Fatalf("new config store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	first := singleConfig()
	if err := store.Save(context.Background(), first); err != nil {
		t.Fatalf("save first: %v", err)
	}

	// Remove the history table so the second save's history step fails inside the
	// transaction, after the gateway_config upsert has already run.
	if _, err := store.db.ExecContext(context.Background(), "DROP TABLE config_history"); err != nil {
		t.Fatalf("drop config_history: %v", err)
	}

	if err := store.Save(context.Background(), fallbackConfig()); err == nil {
		t.Fatal("expected save to fail once history table is missing")
	}

	// The active config must still be the first one: the failed history write
	// rolled the config upsert back.
	loaded, ok, err := store.Load(context.Background())
	if err != nil || !ok {
		t.Fatalf("load: ok=%v err=%v", ok, err)
	}
	if loaded.Strategy.Mode != first.Strategy.Mode {
		t.Fatalf("active config mode = %q, want %q (config write not rolled back)", loaded.Strategy.Mode, first.Strategy.Mode)
	}
}

// TestSQLConfigStore_MigrationSchema confirms the config store runs on the
// migration runner: its own ledger, the baseline table, and config_history are
// all present after construction.
func TestSQLConfigStore_MigrationSchema(t *testing.T) {
	store, err := NewSQLiteConfigStore(t.Context(), filepath.Join(t.TempDir(), "config.db"))
	if err != nil {
		t.Fatalf("new config store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	for _, name := range []string{configLedger, "gateway_config", "config_history"} {
		if !sqliteObjectExists(t, store.db, name) {
			t.Errorf("expected %q to exist after construction", name)
		}
	}
}

// TestSQLConfigStore_AdoptsPreRunnerDatabase confirms a gateway_config database
// created before the runner existed is adopted at the baseline (not re-created)
// and gains config_history, with its existing active config still loadable.
func TestSQLConfigStore_AdoptsPreRunnerDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-config.db")

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	if _, err := raw.ExecContext(context.Background(), `
CREATE TABLE gateway_config (
	id INTEGER PRIMARY KEY,
	config_json TEXT NOT NULL,
	updated_at TIMESTAMP NOT NULL
)`); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	data, err := json.Marshal(fallbackConfig())
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if _, err := raw.ExecContext(context.Background(),
		"INSERT INTO gateway_config(id, config_json, updated_at) VALUES(1, ?, datetime('now'))", string(data)); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw sqlite: %v", err)
	}

	store, err := NewSQLiteConfigStore(t.Context(), path)
	if err != nil {
		t.Fatalf("open store on legacy db: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	loaded, ok, err := store.Load(context.Background())
	if err != nil || !ok {
		t.Fatalf("load legacy config: ok=%v err=%v", ok, err)
	}
	if loaded.Strategy.Mode != aigateway.ModeFallback {
		t.Fatalf("legacy config mode = %q, want %q", loaded.Strategy.Mode, aigateway.ModeFallback)
	}
	if !sqliteObjectExists(t, store.db, "config_history") {
		t.Error("config_history was not created for an adopted legacy database")
	}
}

func TestNewSQLiteConfigStore_FilePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.db")
	store, err := NewSQLiteConfigStore(t.Context(), path)
	if err != nil {
		t.Fatalf("new sqlite config store: %v", err)
	}
	t.Cleanup(func() { _ = store.db.Close() })

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat sqlite file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("expected config store file mode 0600, got %o", perm)
	}
}

type failingConfigStore struct {
	saveErr   error
	deleteErr error
	saved     []aigateway.Config
}

func (s *failingConfigStore) Save(_ context.Context, cfg aigateway.Config) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	s.saved = append(s.saved, cfg)
	return nil
}

func (s *failingConfigStore) Load(context.Context) (aigateway.Config, bool, error) {
	return aigateway.Config{}, false, nil
}
func (s *failingConfigStore) Delete(context.Context) error { return s.deleteErr }

// A reset applies the startup config before clearing the persisted override, so
// a failure to clear leaves the store describing a config the gateway is no
// longer running — and a restart would load it back. The manager records what
// is actually active instead.
func TestGatewayConfigManager_ResetConfig_PersistsWhenDeleteFails(t *testing.T) {
	initial := aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
		Targets:  []aigateway.Target{{VirtualKey: "openai"}},
	}
	gw, err := newTestGateway(t, initial)
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}

	store := &failingConfigStore{deleteErr: errors.New("db down")}
	mgr, err := NewGatewayConfigManager(gw, store)
	if err != nil {
		t.Fatalf("new config manager: %v", err)
	}

	if err := mgr.ResetConfig(context.Background()); err == nil {
		t.Fatal("expected the delete failure to be reported")
	} else if !errors.Is(err, errConfigPersistence) {
		t.Fatalf("expected a persistence-classified error, got: %v", err)
	}

	if len(store.saved) == 0 {
		t.Fatal("delete failed and nothing was persisted: the store still describes the replaced config, so a restart would load it back")
	}
	persisted := store.saved[len(store.saved)-1]
	if persisted.Strategy.Mode != initial.Strategy.Mode || len(persisted.Targets) != len(initial.Targets) {
		t.Fatalf("persisted config does not match the active one: got %+v, want %+v", persisted, initial)
	}
}

func TestGatewayConfigManager_ReloadConfig_RollsBackWhenSaveFails(t *testing.T) {
	initial := aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
		Targets:  []aigateway.Target{{VirtualKey: "openai"}},
	}
	gw, err := newTestGateway(t, initial)
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}

	mgr, err := NewGatewayConfigManager(gw, &failingConfigStore{saveErr: errors.New("db down")})
	if err != nil {
		t.Fatalf("new config manager: %v", err)
	}

	next := aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeFallback},
		Targets:  []aigateway.Target{{VirtualKey: "openai"}, {VirtualKey: "anthropic"}},
	}
	err = mgr.ReloadConfig(context.Background(), next)
	if err == nil {
		t.Fatal("expected save failure")
	}
	if !errors.Is(err, errConfigPersistence) {
		t.Fatalf("expected persistence-classified error, got: %v", err)
	}

	got := mgr.GetConfig()
	if got.Strategy.Mode != initial.Strategy.Mode {
		t.Fatalf("expected rollback to initial mode %q, got %q", initial.Strategy.Mode, got.Strategy.Mode)
	}
	if len(got.Targets) != len(initial.Targets) {
		t.Fatalf("expected rollback target count %d, got %d", len(initial.Targets), len(got.Targets))
	}
}

func TestGatewayConfigManager_ReloadConfig_ClassifiesValidationErrors(t *testing.T) {
	initial := aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
		Targets:  []aigateway.Target{{VirtualKey: "openai"}},
	}
	gw, err := newTestGateway(t, initial)
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	mgr, err := NewGatewayConfigManager(gw, nil)
	if err != nil {
		t.Fatalf("new config manager: %v", err)
	}

	invalid := aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: "invalid"},
		Targets:  []aigateway.Target{{VirtualKey: "openai"}},
	}
	err = mgr.ReloadConfig(context.Background(), invalid)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !errors.Is(err, errConfigValidation) {
		t.Fatalf("expected validation-classified error, got: %v", err)
	}
}

func TestGatewayConfigManager_NilGateway(t *testing.T) {
	_, err := NewGatewayConfigManager(nil, nil)
	if err == nil {
		t.Error("expected error for nil gateway")
	}
}

func TestGatewayConfigManager_NilStore(t *testing.T) {
	cfg := aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
		Targets:  []aigateway.Target{{VirtualKey: "openai"}},
	}
	gw, err := newTestGateway(t, cfg)
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	mgr, err := NewGatewayConfigManager(gw, nil)
	if err != nil {
		t.Fatalf("NewGatewayConfigManager(nil store) failed: %v", err)
	}
	if mgr.GetConfig().Strategy.Mode != aigateway.ModeSingle {
		t.Error("expected single mode after creation with nil store")
	}
}

type successConfigStore struct {
	cfg aigateway.Config
}

func (s *successConfigStore) Save(_ context.Context, c aigateway.Config) error { s.cfg = c; return nil }
func (s *successConfigStore) Load(context.Context) (aigateway.Config, bool, error) {
	return s.cfg, s.cfg.Strategy.Mode != "", nil
}
func (s *successConfigStore) Delete(context.Context) error { s.cfg = aigateway.Config{}; return nil }

func TestGatewayConfigManager_WithPersistedConfig(t *testing.T) {
	initial := aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
		Targets:  []aigateway.Target{{VirtualKey: "openai"}},
	}
	persisted := aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeFallback},
		Targets:  []aigateway.Target{{VirtualKey: "openai"}, {VirtualKey: "anthropic"}},
	}
	store := &successConfigStore{cfg: persisted}

	gw, err := newTestGateway(t, initial)
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	mgr, err := NewGatewayConfigManager(gw, store)
	if err != nil {
		t.Fatalf("NewGatewayConfigManager with persisted config failed: %v", err)
	}
	if mgr.GetConfig().Strategy.Mode != aigateway.ModeFallback {
		t.Errorf("expected persisted mode %q to be applied, got %q", aigateway.ModeFallback, mgr.GetConfig().Strategy.Mode)
	}
}

func TestGatewayConfigManager_ReloadConfig_Success(t *testing.T) {
	initial := aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
		Targets:  []aigateway.Target{{VirtualKey: "openai"}},
	}
	gw, err := newTestGateway(t, initial)
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	mgr, err := NewGatewayConfigManager(gw, nil)
	if err != nil {
		t.Fatalf("new config manager: %v", err)
	}

	next := aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeFallback},
		Targets:  []aigateway.Target{{VirtualKey: "openai"}, {VirtualKey: "anthropic"}},
	}
	if err := mgr.ReloadConfig(context.Background(), next); err != nil {
		t.Fatalf("ReloadConfig failed: %v", err)
	}
	if mgr.GetConfig().Strategy.Mode != aigateway.ModeFallback {
		t.Errorf("expected fallback mode after reload, got %q", mgr.GetConfig().Strategy.Mode)
	}
}

func TestGatewayConfigManager_ResetConfig(t *testing.T) {
	initial := aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
		Targets:  []aigateway.Target{{VirtualKey: "openai"}},
	}
	gw, err := newTestGateway(t, initial)
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	store := &successConfigStore{}
	mgr, err := NewGatewayConfigManager(gw, store)
	if err != nil {
		t.Fatalf("new config manager: %v", err)
	}

	// Reload to a different config.
	next := aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeFallback},
		Targets:  []aigateway.Target{{VirtualKey: "openai"}, {VirtualKey: "anthropic"}},
	}
	if err := mgr.ReloadConfig(context.Background(), next); err != nil {
		t.Fatalf("ReloadConfig failed: %v", err)
	}

	// Reset to initial.
	if err := mgr.ResetConfig(context.Background()); err != nil {
		t.Fatalf("ResetConfig failed: %v", err)
	}
	if mgr.GetConfig().Strategy.Mode != aigateway.ModeSingle {
		t.Errorf("expected reset to single mode, got %q", mgr.GetConfig().Strategy.Mode)
	}
}

// TestGatewayConfigManager_Ping_DoesNotDoubleWrapStoreError proves the manager
// surfaces the underlying store's ping error as-is: SQLConfigStore.Ping already
// prefixes its error with "config store ping: ", so a second wrap at the
// manager level would make that prefix appear twice in the /readyz body.
func TestGatewayConfigManager_Ping_DoesNotDoubleWrapStoreError(t *testing.T) {
	store, err := NewSQLiteConfigStore(t.Context(), filepath.Join(t.TempDir(), "config.db"))
	if err != nil {
		t.Fatalf("new config store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	gw, err := newTestGateway(t, singleConfig())
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	mgr, err := NewGatewayConfigManager(gw, store)
	if err != nil {
		t.Fatalf("new config manager: %v", err)
	}

	// Close the underlying connection directly so the store's own Ping fails
	// and returns its wrapped error, without going through store.Close().
	if err := store.db.Close(); err != nil {
		t.Fatalf("close underlying db: %v", err)
	}

	err = mgr.Ping(context.Background())
	if err == nil {
		t.Fatal("expected ping error once the store is unreachable")
	}
	const wantPrefix = "config store ping: "
	if got := strings.Count(err.Error(), wantPrefix); got != 1 {
		t.Fatalf("Ping() error = %q, want %q to appear exactly once, appeared %d times", err.Error(), wantPrefix, got)
	}
}

// TestGatewayConfigManager_Ping_NilReceiver proves a nil *GatewayConfigManager
// reports an error instead of panicking: readiness must fail closed for an
// uninitialized manager, not crash on the m.store dereference.
func TestGatewayConfigManager_Ping_NilReceiver(t *testing.T) {
	var mgr *GatewayConfigManager
	if err := mgr.Ping(context.Background()); err == nil {
		t.Fatal("expected error pinging a nil config manager")
	}
}

// TestSQLConfigStore_Ping_NilReceiver proves a nil *SQLConfigStore reports an
// error instead of panicking: an uninitialized store is not reachable, so
// readiness must fail closed rather than crash.
func TestSQLConfigStore_Ping_NilReceiver(t *testing.T) {
	var store *SQLConfigStore
	if err := store.Ping(context.Background()); err == nil {
		t.Fatal("expected error pinging a nil config store")
	}
}

// TestSQLConfigStore_Ping_NilDB covers the other uninitialized shape: a
// non-nil store whose db was never opened.
func TestSQLConfigStore_Ping_NilDB(t *testing.T) {
	store := &SQLConfigStore{}
	if err := store.Ping(context.Background()); err == nil {
		t.Fatal("expected error pinging a config store with a nil db")
	}
}

// TestSQLConfigStore_LoadHistoryBoundsRowsRead proves LoadHistory reads at most
// maxConfigHistoryEntries rows — the newest ones, still ascending — and that
// bounding the read destroys nothing: every seeded row is still in the table.
func TestSQLConfigStore_LoadHistoryBoundsRowsRead(t *testing.T) {
	store, err := NewSQLiteConfigStore(t.Context(), filepath.Join(t.TempDir(), "config.db"))
	if err != nil {
		t.Fatalf("new config store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	const seeded = maxConfigHistoryEntries + 50
	raw, err := json.Marshal(singleConfig())
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	now := time.Now().UTC()
	for v := 1; v <= seeded; v++ {
		if _, err := store.db.ExecContext(context.Background(),
			"INSERT INTO config_history(version, config_json, updated_at) VALUES(?, ?, ?)",
			v, string(raw), now); err != nil {
			t.Fatalf("seed history version %d: %v", v, err)
		}
	}

	history, err := store.LoadHistory(context.Background())
	if err != nil {
		t.Fatalf("load history: %v", err)
	}
	if len(history) != maxConfigHistoryEntries {
		t.Fatalf("history len = %d, want %d", len(history), maxConfigHistoryEntries)
	}
	if got, want := history[0].Version, seeded-maxConfigHistoryEntries+1; got != want {
		t.Fatalf("oldest returned version = %d, want %d (should return the newest window)", got, want)
	}
	if got := history[len(history)-1].Version; got != seeded {
		t.Fatalf("newest returned version = %d, want %d", got, seeded)
	}
	for i := range history {
		if want := history[0].Version + i; history[i].Version != want {
			t.Fatalf("history[%d].Version = %d, want %d (must stay ascending)", i, history[i].Version, want)
		}
	}

	// The bound is a read limit, never a retention policy: nothing was deleted.
	var total int
	if err := store.db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM config_history").Scan(&total); err != nil {
		t.Fatalf("count config_history: %v", err)
	}
	if total != seeded {
		t.Fatalf("config_history has %d rows, want %d: rows must never be deleted", total, seeded)
	}
}
