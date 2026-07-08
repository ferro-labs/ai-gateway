package bootstrap

import (
	"path/filepath"
	"strings"
	"testing"

	aigateway "github.com/ferro-labs/ai-gateway"
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
	gw, err := aigateway.New(aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeFallback},
		Targets:  []aigateway.Target{{VirtualKey: "test"}},
	})
	if err != nil {
		t.Fatalf("failed to create test gateway: %v", err)
	}
	return gw
}
