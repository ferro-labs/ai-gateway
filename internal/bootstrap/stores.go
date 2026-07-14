// Package bootstrap provides env-driven factory functions for persistence backends.
package bootstrap

import (
	"context"
	"fmt"
	"os"
	"strings"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/internal/admin"
	"github.com/ferro-labs/ai-gateway/internal/requestlog"
)

// Backend name constants returned alongside created stores.
const (
	BackendMemory      = "memory"
	BackendSQLite      = "sqlite"
	BackendPostgres    = "postgres"
	backendPostgresSQL = "postgresql"
)

// CreateKeyStoreFromEnv builds an admin key store from API_KEY_STORE_BACKEND / API_KEY_STORE_DSN env vars.
func CreateKeyStoreFromEnv(ctx context.Context) (admin.Store, string, error) {
	backend := strings.ToLower(strings.TrimSpace(os.Getenv("API_KEY_STORE_BACKEND")))
	if backend == "" {
		backend = BackendMemory
	}

	storeDSN := strings.TrimSpace(os.Getenv("API_KEY_STORE_DSN"))

	switch backend {
	case BackendMemory, "in-memory", "inmemory":
		return admin.NewKeyStore(), BackendMemory, nil
	case BackendSQLite:
		store, err := admin.NewSQLiteStore(ctx, storeDSN)
		if err != nil {
			return nil, "", err
		}
		return store, BackendSQLite, nil
	case BackendPostgres, backendPostgresSQL:
		store, err := admin.NewPostgresStore(ctx, storeDSN)
		if err != nil {
			return nil, "", err
		}
		return store, BackendPostgres, nil
	default:
		return nil, "", fmt.Errorf("unsupported API key store backend %q", backend)
	}
}

// CreateRequestLogReaderFromEnv builds a request log reader from REQUEST_LOG_STORE_BACKEND / REQUEST_LOG_STORE_DSN env vars.
func CreateRequestLogReaderFromEnv(ctx context.Context) (requestlog.Reader, requestlog.Maintainer, string, error) {
	backend := strings.ToLower(strings.TrimSpace(os.Getenv("REQUEST_LOG_STORE_BACKEND")))
	if backend == "" {
		return nil, nil, "disabled", nil
	}

	dsn := strings.TrimSpace(os.Getenv("REQUEST_LOG_STORE_DSN"))

	switch backend {
	case BackendSQLite:
		reader, err := requestlog.NewSQLiteWriter(ctx, dsn)
		if err != nil {
			return nil, nil, "", err
		}
		return reader, reader, BackendSQLite, nil
	case BackendPostgres, backendPostgresSQL:
		reader, err := requestlog.NewPostgresWriter(ctx, dsn)
		if err != nil {
			return nil, nil, "", err
		}
		return reader, reader, BackendPostgres, nil
	default:
		return nil, nil, "", fmt.Errorf("unsupported request log store backend %q", backend)
	}
}

// CreateConfigManagerFromEnv builds a config manager from CONFIG_STORE_BACKEND / CONFIG_STORE_DSN env vars.
func CreateConfigManagerFromEnv(ctx context.Context, gw *aigateway.Gateway) (admin.ConfigManager, string, error) {
	backend := strings.ToLower(strings.TrimSpace(os.Getenv("CONFIG_STORE_BACKEND")))
	if backend == "" {
		backend = BackendMemory
	}

	dsn := strings.TrimSpace(os.Getenv("CONFIG_STORE_DSN"))

	switch backend {
	case BackendMemory, "in-memory", "inmemory":
		manager, err := admin.NewGatewayConfigManager(gw, nil)
		if err != nil {
			return nil, "", err
		}
		return manager, BackendMemory, nil
	case BackendSQLite:
		store, err := admin.NewSQLiteConfigStore(ctx, dsn)
		if err != nil {
			return nil, "", err
		}
		manager, err := admin.NewGatewayConfigManager(gw, store)
		if err != nil {
			_ = store.Close()
			return nil, "", err
		}
		return manager, BackendSQLite, nil
	case BackendPostgres, backendPostgresSQL:
		store, err := admin.NewPostgresConfigStore(ctx, dsn)
		if err != nil {
			return nil, "", err
		}
		manager, err := admin.NewGatewayConfigManager(gw, store)
		if err != nil {
			_ = store.Close()
			return nil, "", err
		}
		return manager, BackendPostgres, nil
	default:
		return nil, "", fmt.Errorf("unsupported config store backend %q", backend)
	}
}
