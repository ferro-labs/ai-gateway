//go:build integration
// +build integration

package integration

import (
	"testing"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/internal/bootstrap"
)

func TestBootstrap_KeyStore_Postgres(t *testing.T) {
	t.Setenv("API_KEY_STORE_BACKEND", "postgres")
	t.Setenv("API_KEY_STORE_DSN", testDSN)

	store, backend, err := bootstrap.CreateKeyStoreFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store == nil {
		t.Fatal("expected non-nil store")
	}
	if backend != bootstrap.BackendPostgres {
		t.Fatalf("expected %q, got %q", bootstrap.BackendPostgres, backend)
	}
}

func TestBootstrap_KeyStore_PostgreSQLAlias(t *testing.T) {
	t.Setenv("API_KEY_STORE_BACKEND", "postgresql")
	t.Setenv("API_KEY_STORE_DSN", testDSN)

	store, backend, err := bootstrap.CreateKeyStoreFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store == nil {
		t.Fatal("expected non-nil store")
	}
	if backend != bootstrap.BackendPostgres {
		t.Fatalf("expected %q, got %q", bootstrap.BackendPostgres, backend)
	}
}

func TestBootstrap_RequestLogReader_Postgres(t *testing.T) {
	t.Setenv("REQUEST_LOG_STORE_BACKEND", "postgres")
	t.Setenv("REQUEST_LOG_STORE_DSN", testDSN)

	reader, maintainer, backend, err := bootstrap.CreateRequestLogReaderFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reader == nil || maintainer == nil {
		t.Fatal("expected non-nil reader and maintainer")
	}
	if backend != bootstrap.BackendPostgres {
		t.Fatalf("expected %q, got %q", bootstrap.BackendPostgres, backend)
	}
}

func TestBootstrap_ConfigManager_Postgres(t *testing.T) {
	t.Setenv("CONFIG_STORE_BACKEND", "postgres")
	t.Setenv("CONFIG_STORE_DSN", testDSN)

	gw, err := aigateway.New(aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeFallback},
		Targets:  []aigateway.Target{{VirtualKey: "test"}},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}

	mgr, backend, err := bootstrap.CreateConfigManagerFromEnv(gw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mgr == nil {
		t.Fatal("expected non-nil config manager")
	}
	if backend != bootstrap.BackendPostgres {
		t.Fatalf("expected %q, got %q", bootstrap.BackendPostgres, backend)
	}
}
