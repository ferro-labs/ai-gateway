package bootstrap

import (
	"path/filepath"
	"strings"
	"testing"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/config"
)

func TestCreateKeyStoreFromEnv_DefaultsToMemory(t *testing.T) {
	t.Setenv("API_KEY_STORE_BACKEND", "")
	store, backend, err := CreateKeyStoreFromEnv(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store == nil {
		t.Fatal("expected non-nil store")
	}
	if backend != BackendMemory {
		t.Fatalf("expected %q, got %q", BackendMemory, backend)
	}
}

func TestCreateKeyStoreFromEnv_MemoryAliases(t *testing.T) {
	for _, alias := range []string{"memory", "in-memory", "inmemory", "MEMORY", " Memory "} {
		t.Run(alias, func(t *testing.T) {
			t.Setenv("API_KEY_STORE_BACKEND", alias)
			store, backend, err := CreateKeyStoreFromEnv(t.Context())
			if err != nil {
				t.Fatalf("unexpected error for alias %q: %v", alias, err)
			}
			if store == nil {
				t.Fatal("expected non-nil store")
			}
			if backend != BackendMemory {
				t.Fatalf("expected %q, got %q", BackendMemory, backend)
			}
		})
	}
}

func TestCreateKeyStoreFromEnv_SQLite(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "keys.db")
	t.Setenv("API_KEY_STORE_BACKEND", "sqlite")
	t.Setenv("API_KEY_STORE_DSN", dbPath)

	store, backend, err := CreateKeyStoreFromEnv(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store == nil {
		t.Fatal("expected non-nil store")
	}
	if backend != BackendSQLite {
		t.Fatalf("expected %q, got %q", BackendSQLite, backend)
	}
}

func TestCreateKeyStoreFromEnv_UnsupportedBackend(t *testing.T) {
	t.Setenv("API_KEY_STORE_BACKEND", "redis")
	_, _, err := CreateKeyStoreFromEnv(t.Context())
	if err == nil {
		t.Fatal("expected error for unsupported backend")
	}
}

func TestCreateSessionStoreFromEnvDefaultsToMemory(t *testing.T) {
	t.Setenv("API_KEY_STORE_BACKEND", "")
	store, backend, err := CreateSessionStoreFromEnv(t.Context())
	if err != nil {
		t.Fatalf("CreateSessionStoreFromEnv: %v", err)
	}
	if backend != BackendMemory {
		t.Fatalf("backend = %q, want %q", backend, BackendMemory)
	}
	if store == nil {
		t.Fatal("store is nil; sessions must work with no database configured")
	}
}

func TestCreateSessionStoreFromEnvSQLite(t *testing.T) {
	t.Setenv("API_KEY_STORE_BACKEND", "sqlite")
	t.Setenv("API_KEY_STORE_DSN", t.TempDir()+"/keys.db")
	store, backend, err := CreateSessionStoreFromEnv(t.Context())
	if err != nil {
		t.Fatalf("CreateSessionStoreFromEnv: %v", err)
	}
	if backend != BackendSQLite {
		t.Fatalf("backend = %q, want %q", backend, BackendSQLite)
	}
	if store == nil {
		t.Fatal("store is nil")
	}
}

// TestCreateSessionStoreFromEnvSessionDSN pins the derived session DSN so a
// SQLite key file (something operators copy between machines) never carries
// live session rows: sessions get their own database file alongside it.
//
// Two forms the earlier filepath.Ext-based derivation mishandled are covered
// here: a "?query" suffix (busy_timeout, journal_mode, ...) must survive onto
// the derived DSN rather than being silently trimmed away with the
// extension, and an in-memory DSN (":memory:") must stay in memory rather
// than becoming a real on-disk file literally named ":memory:-sessions.db".
func TestCreateSessionStoreFromEnvSessionDSN(t *testing.T) {
	tests := []struct {
		name   string
		keyDSN string
		want   string
	}{
		{name: "empty DSN falls back to the store's own default filename", keyDSN: "", want: ""},
		{name: "plain path sibling file next to the key store", keyDSN: "/data/ferrogw-keys.db", want: "/data/ferrogw-keys-sessions.db"},
		{name: "path with no extension", keyDSN: "/data/keys", want: "/data/keys-sessions.db"},
		{name: "relative path", keyDSN: "keys.db", want: "keys-sessions.db"},
		{name: "directory name containing a dot", keyDSN: "/data/app.v2/keys.db", want: "/data/app.v2/keys-sessions.db"},
		{
			name:   "file: URI with a query preserves the query",
			keyDSN: "file:/data/keys.db?_pragma=busy_timeout(5000)",
			want:   "file:/data/keys-sessions.db?_pragma=busy_timeout(5000)",
		},
		{name: ":memory: stays in memory rather than becoming a real file", keyDSN: ":memory:", want: ":memory:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sessionDSN(tt.keyDSN)
			if got != tt.want {
				t.Fatalf("sessionDSN(%q) = %q, want %q", tt.keyDSN, got, tt.want)
			}
			// The derived DSN must never resolve to the same DSN the key
			// store itself uses -- the whole point of deriving a sibling
			// name. ":memory:" is the one deliberate exception: it names no
			// backing file at all, so returning it unchanged cannot put a
			// revocable session row in a file an operator copies around.
			if got == tt.keyDSN && tt.keyDSN != "" && tt.keyDSN != ":memory:" {
				t.Fatalf("sessionDSN(%q) = %q: must not map onto the key store's own DSN", tt.keyDSN, got)
			}
		})
	}
}

func TestCreateSessionStoreFromEnv_UnsupportedBackend(t *testing.T) {
	t.Setenv("API_KEY_STORE_BACKEND", "redis")
	_, _, err := CreateSessionStoreFromEnv(t.Context())
	if err == nil {
		t.Fatal("expected error for unsupported backend")
	}
}

func TestCreateRequestLogReaderFromEnv_DefaultDisabled(t *testing.T) {
	t.Setenv("REQUEST_LOG_STORE_BACKEND", "")
	reader, maintainer, backend, err := CreateRequestLogReaderFromEnv(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reader != nil || maintainer != nil {
		t.Fatal("expected nil reader and maintainer when disabled")
	}
	if backend != "disabled" {
		t.Fatalf("expected %q, got %q", "disabled", backend)
	}
}

func TestCreateRequestLogReaderFromEnv_SQLite(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "logs.db")
	t.Setenv("REQUEST_LOG_STORE_BACKEND", "sqlite")
	t.Setenv("REQUEST_LOG_STORE_DSN", dbPath)

	reader, maintainer, backend, err := CreateRequestLogReaderFromEnv(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reader == nil || maintainer == nil {
		t.Fatal("expected non-nil reader and maintainer")
	}
	if backend != BackendSQLite {
		t.Fatalf("expected %q, got %q", BackendSQLite, backend)
	}
}

func TestCreateRequestLogReaderFromEnv_UnsupportedBackend(t *testing.T) {
	t.Setenv("REQUEST_LOG_STORE_BACKEND", "redis")
	_, _, _, err := CreateRequestLogReaderFromEnv(t.Context())
	if err == nil {
		t.Fatal("expected error for unsupported backend")
	}
}

func TestCreateConfigManagerFromEnv_DefaultsToMemory(t *testing.T) {
	t.Setenv("CONFIG_STORE_BACKEND", "")
	gw := newTestGateway(t)

	mgr, backend, err := CreateConfigManagerFromEnv(t.Context(), gw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mgr == nil {
		t.Fatal("expected non-nil config manager")
	}
	if backend != BackendMemory {
		t.Fatalf("expected %q, got %q", BackendMemory, backend)
	}
}

func TestCreateConfigManagerFromEnv_SQLite(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "config.db")
	t.Setenv("CONFIG_STORE_BACKEND", "sqlite")
	t.Setenv("CONFIG_STORE_DSN", dbPath)
	gw := newTestGateway(t)

	mgr, backend, err := CreateConfigManagerFromEnv(t.Context(), gw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mgr == nil {
		t.Fatal("expected non-nil config manager")
	}
	if backend != BackendSQLite {
		t.Fatalf("expected %q, got %q", BackendSQLite, backend)
	}
}

func TestCreateConfigManagerFromEnv_UnsupportedBackend(t *testing.T) {
	t.Setenv("CONFIG_STORE_BACKEND", "redis")
	gw := newTestGateway(t)

	_, _, err := CreateConfigManagerFromEnv(t.Context(), gw)
	if err == nil {
		t.Fatal("expected error for unsupported backend")
	}
}

func TestCreateConfigManagerFromEnv_PostgresqlAlias(t *testing.T) {
	t.Setenv("CONFIG_STORE_BACKEND", "postgresql")
	t.Setenv("CONFIG_STORE_DSN", "postgresql://invalid:5432/test")
	gw := newTestGateway(t)

	_, _, err := CreateConfigManagerFromEnv(t.Context(), gw)
	if err == nil {
		t.Skip("postgres not available, but alias was recognized")
	}
	if strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("postgresql alias was not recognized: %v", err)
	}
}

func newTestGateway(t *testing.T) *aigateway.Gateway {
	t.Helper()
	gw, err := aigateway.New(config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeFallback},
		Targets:  []config.Target{{VirtualKey: "test"}},
	})
	if err != nil {
		t.Fatalf("failed to create test gateway: %v", err)
	}
	t.Cleanup(func() {
		if err := gw.Close(); err != nil {
			t.Errorf("close gateway: %v", err)
		}
	})
	return gw
}
