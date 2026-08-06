package repository

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/pkg/logger"
)

// A rollback and an ordinary update produce the same shape in config_history —
// a new version carrying an older config — so without recorded provenance the
// durable trail cannot tell them apart, and the dashboard's "Rolled back from"
// column read "—" for every row on every deployment that keeps a config store.

func newRollbackConfigStore(t *testing.T) *SQLConfigStore {
	t.Helper()
	store, err := NewSQLiteConfigStore(t.Context(), filepath.Join(t.TempDir(), "config.db"))
	if err != nil {
		t.Fatalf("open config store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestConfigHistory_RecordsRollbackProvenance(t *testing.T) {
	store := newRollbackConfigStore(t)

	if err := store.Save(t.Context(), singleConfig()); err != nil {
		t.Fatalf("save v1: %v", err)
	}
	if err := store.Save(t.Context(), fallbackConfig()); err != nil {
		t.Fatalf("save v2: %v", err)
	}
	// Restoring v1 supersedes v2, which is the fact the column carries.
	if err := store.Save(WithRollbackFrom(t.Context(), 2), singleConfig()); err != nil {
		t.Fatalf("save rollback: %v", err)
	}

	history, err := store.LoadHistory(t.Context())
	if err != nil {
		t.Fatalf("load history: %v", err)
	}
	if len(history) != 3 {
		t.Fatalf("history len = %d, want 3", len(history))
	}

	for _, i := range []int{0, 1} {
		if history[i].RolledBackFrom != nil {
			t.Errorf("version %d reports rolled_back_from = %d; an ordinary update supersedes nothing",
				history[i].Version, *history[i].RolledBackFrom)
		}
	}
	if history[2].RolledBackFrom == nil {
		t.Fatal("the rollback is indistinguishable from an ordinary update in the durable trail")
	}
	if got := *history[2].RolledBackFrom; got != 2 {
		t.Errorf("rolled_back_from = %d, want 2", got)
	}
}

// The provenance must not leak between writes: a context marked for one
// rollback is not the context of the save that follows it.
func TestConfigHistory_ResetRecordsNoRollbackProvenance(t *testing.T) {
	store := newRollbackConfigStore(t)

	if err := store.Save(WithRollbackFrom(t.Context(), 1), singleConfig()); err != nil {
		t.Fatalf("save rollback: %v", err)
	}
	if err := store.DeleteAndRecord(t.Context(), fallbackConfig()); err != nil {
		t.Fatalf("delete and record: %v", err)
	}

	history, err := store.LoadHistory(t.Context())
	if err != nil {
		t.Fatalf("load history: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("history len = %d, want 2", len(history))
	}
	if history[1].RolledBackFrom != nil {
		t.Errorf("the reset recorded rolled_back_from = %d; a reset restores the startup config, it supersedes no version",
			*history[1].RolledBackFrom)
	}
}

// The column has to arrive on a database that already holds rows, which is
// every database this migration will actually meet. An empty one proves only
// that the DDL parses.
func TestConfigHistory_UpgradesAPopulatedDatabaseWrittenBeforeRollbackProvenance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre-rollback-config.db")

	// The v3 shape by hand — config_history with actor and no
	// rolled_back_from — carrying two rows and a live active config.
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
		t.Fatalf("create gateway_config: %v", err)
	}
	if _, err := raw.ExecContext(context.Background(), `
CREATE TABLE config_history (
	version INTEGER PRIMARY KEY,
	config_json TEXT NOT NULL,
	updated_at TIMESTAMP NOT NULL,
	actor TEXT NOT NULL DEFAULT ''
)`); err != nil {
		t.Fatalf("create config_history: %v", err)
	}
	single, err := json.Marshal(singleConfig())
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	fallback, err := json.Marshal(fallbackConfig())
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if _, err := raw.ExecContext(context.Background(),
		"INSERT INTO gateway_config(id, config_json, updated_at) VALUES(1, ?, datetime('now'))",
		string(fallback)); err != nil {
		t.Fatalf("seed active config: %v", err)
	}
	for version, data := range map[int]string{1: string(single), 2: string(fallback)} {
		if _, err := raw.ExecContext(context.Background(),
			"INSERT INTO config_history(version, config_json, updated_at, actor) VALUES(?, ?, datetime('now'), 'alice (key-1)')",
			version, data); err != nil {
			t.Fatalf("seed history row %d: %v", version, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw sqlite: %v", err)
	}

	store, err := NewSQLiteConfigStore(t.Context(), path)
	if err != nil {
		t.Fatalf("open store on populated pre-rollback db: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	active, ok, err := store.Load(t.Context())
	if err != nil || !ok {
		t.Fatalf("load active config after upgrade: ok=%v err=%v", ok, err)
	}
	if active.Strategy.Mode != config.ModeFallback {
		t.Errorf("active config mode = %q, want %q", active.Strategy.Mode, config.ModeFallback)
	}

	history, err := store.LoadHistory(t.Context())
	if err != nil {
		t.Fatalf("load history after upgrade: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("history has %d rows, want the 2 that predated the upgrade", len(history))
	}
	for _, entry := range history {
		if entry.RolledBackFrom != nil {
			t.Errorf("version %d was backfilled with rolled_back_from = %d; nothing recorded that",
				entry.Version, *entry.RolledBackFrom)
		}
		// The upgrade must not disturb what the rows already carried.
		if entry.Actor != "alice (key-1)" {
			t.Errorf("version %d actor = %q after upgrade, want it unchanged", entry.Version, entry.Actor)
		}
	}

	// The new column is usable on the upgraded database, not merely present.
	if err := store.Save(WithRollbackFrom(t.Context(), 2), singleConfig()); err != nil {
		t.Fatalf("save rollback after upgrade: %v", err)
	}
	history, err = store.LoadHistory(t.Context())
	if err != nil {
		t.Fatalf("reload history: %v", err)
	}
	newest := history[len(history)-1]
	if newest.RolledBackFrom == nil || *newest.RolledBackFrom != 2 {
		t.Fatalf("rollback provenance not recorded on the upgraded database: %v", newest.RolledBackFrom)
	}
}

// A config persist that fails and then cannot be reverted leaves the gateway
// running one config while the store holds another — and the store wins on the
// next boot. Nothing in the process can decide which is right, so the instance
// has to stop claiming it can serve.
func TestGatewayConfigManager_ReloadConfig_FailedRevertStopsReportingReady(t *testing.T) {
	// The running config carries a plugin whose config names an environment
	// variable. ${VAR} is resolved when the plugin is CONSTRUCTED, so the
	// variable going away is what makes re-applying this same config fail —
	// the shape of any plugin factory that succeeded once and cannot again.
	t.Setenv("FERRO_TEST_BLOCKED_WORD", "hunter2")
	running := config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "openai"}},
		Plugins: []config.PluginConfig{{
			Name:    "word-filter",
			Type:    "guardrail",
			Stage:   "before_request",
			Enabled: true,
			Config:  map[string]any{"blocked_words": []any{"${FERRO_TEST_BLOCKED_WORD}"}},
		}},
	}

	gw, err := newTestGateway(t, running)
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}

	mgr, err := NewGatewayConfigManager(gw, &failingConfigStore{saveErr: errors.New("db down")})
	if err != nil {
		t.Fatalf("new config manager: %v", err)
	}
	if err := mgr.Ping(t.Context()); err != nil {
		t.Fatalf("manager should start ready: %v", err)
	}

	// From here the running config can no longer be rebuilt, so the apply of
	// the new one succeeds, the persist fails, and the revert fails too.
	if err := os.Unsetenv("FERRO_TEST_BLOCKED_WORD"); err != nil {
		t.Fatalf("unset env: %v", err)
	}

	err = mgr.ReloadConfig(t.Context(), fallbackConfig())
	if err == nil {
		t.Fatal("expected the persist failure to be reported")
	}
	if !errors.Is(err, ErrConfigPersistence) {
		t.Fatalf("expected a persistence-classified error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "revert applied config failed") {
		t.Fatalf("this test needs the revert to fail; it did not: %v", err)
	}

	if pingErr := mgr.Ping(t.Context()); pingErr == nil {
		t.Fatal("the running config and the store disagree and the instance still reports ready; a restart would silently adopt the store's config")
	}
}

// A literal secret is persisted exactly as written while GET /admin/config
// answers "[REDACTED]" for the same field, which reads as protection it does
// not have. The write is not blocked — scrubbing would store a config that
// cannot run, and rejecting would break configs that work today — so the
// warning naming the field is the whole signal.
func TestGatewayConfigManager_ReloadConfig_WarnsAboutAnInlinedLiteral(t *testing.T) {
	inlined := func(value string) config.Config {
		return config.Config{
			Strategy: config.StrategyConfig{Mode: config.ModeSingle},
			Targets:  []config.Target{{VirtualKey: "openai"}},
			Observability: config.ObservabilityConfig{
				Exporters: []config.ExporterConfig{{
					Name:    "langsmith",
					Enabled: false,
					Config:  map[string]any{"api_key": value},
				}},
			},
		}
	}

	for _, tc := range []struct {
		name string
		cfg  config.Config
		warn bool
	}{
		{"an inlined literal is reported", inlined("sk-live-abcdefghijklmnopqrstuvwxyz"), true},
		{"a ${VAR} reference is not", inlined("${LANGSMITH_API_KEY}"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gw, err := newTestGateway(t, singleConfig())
			if err != nil {
				t.Fatalf("new gateway: %v", err)
			}
			mgr, err := NewGatewayConfigManager(gw, &failingConfigStore{})
			if err != nil {
				t.Fatalf("new config manager: %v", err)
			}

			buf := &bytes.Buffer{}
			previous := logger.Default()
			logger.SetDefault(logger.New(logger.Options{Output: buf}))
			t.Cleanup(func() { logger.SetDefault(previous) })

			if err := mgr.ReloadConfig(t.Context(), tc.cfg); err != nil {
				t.Fatalf("reload config: %v", err)
			}

			warned := strings.Contains(buf.String(), "withheld from GET /admin/config")
			if warned != tc.warn {
				t.Fatalf("warned = %v, want %v; log was:\n%s", warned, tc.warn, buf.String())
			}
			if !tc.warn {
				return
			}
			if !strings.Contains(buf.String(), "observability") {
				t.Errorf("the warning does not name the offending field; log was:\n%s", buf.String())
			}
			// Naming the field must not quote its value.
			if strings.Contains(buf.String(), "sk-live-abcdefghijklmnopqrstuvwxyz") {
				t.Errorf("the warning quoted the secret it is warning about; log was:\n%s", buf.String())
			}
		})
	}
}
