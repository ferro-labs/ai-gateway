package main

import (
	"fmt"
	"os"
	"strings"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/internal/admin"
	"github.com/ferro-labs/ai-gateway/internal/requestlog"
)

const (
	backendMemory      = "memory"
	backendSQLite      = "sqlite"
	backendPostgres    = "postgres"
	backendPostgresSQL = "postgresql"
)

func createKeyStoreFromEnv() (admin.Store, string, error) {
	backend := strings.ToLower(strings.TrimSpace(os.Getenv("API_KEY_STORE_BACKEND")))
	if backend == "" {
		backend = backendMemory
	}

	storeDSN := strings.TrimSpace(os.Getenv("API_KEY_STORE_DSN"))

	switch backend {
	case backendMemory, "in-memory", "inmemory":
		return admin.NewKeyStore(), backendMemory, nil
	case backendSQLite:
		store, err := admin.NewSQLiteStore(storeDSN)
		if err != nil {
			return nil, "", err
		}
		return store, backendSQLite, nil
	case backendPostgres, backendPostgresSQL:
		store, err := admin.NewPostgresStore(storeDSN)
		if err != nil {
			return nil, "", err
		}
		return store, backendPostgres, nil
	default:
		return nil, "", fmt.Errorf("unsupported API key store backend %q", backend)
	}
}

func createRequestLogReaderFromEnv() (requestlog.Reader, requestlog.Maintainer, string, error) {
	backend := strings.ToLower(strings.TrimSpace(os.Getenv("REQUEST_LOG_STORE_BACKEND")))
	if backend == "" {
		return nil, nil, "disabled", nil
	}

	dsn := strings.TrimSpace(os.Getenv("REQUEST_LOG_STORE_DSN"))

	switch backend {
	case backendSQLite:
		reader, err := requestlog.NewSQLiteWriter(dsn)
		if err != nil {
			return nil, nil, "", err
		}
		return reader, reader, backendSQLite, nil
	case backendPostgres, backendPostgresSQL:
		reader, err := requestlog.NewPostgresWriter(dsn)
		if err != nil {
			return nil, nil, "", err
		}
		return reader, reader, backendPostgres, nil
	default:
		return nil, nil, "", fmt.Errorf("unsupported request log store backend %q", backend)
	}
}

func createConfigManagerFromEnv(gw *aigateway.Gateway) (admin.ConfigManager, string, error) {
	backend := strings.ToLower(strings.TrimSpace(os.Getenv("CONFIG_STORE_BACKEND")))
	if backend == "" {
		backend = backendMemory
	}

	dsn := strings.TrimSpace(os.Getenv("CONFIG_STORE_DSN"))

	switch backend {
	case backendMemory, "in-memory", "inmemory":
		manager, err := admin.NewGatewayConfigManager(gw, nil)
		if err != nil {
			return nil, "", err
		}
		return manager, backendMemory, nil
	case backendSQLite:
		store, err := admin.NewSQLiteConfigStore(dsn)
		if err != nil {
			return nil, "", err
		}
		manager, err := admin.NewGatewayConfigManager(gw, store)
		if err != nil {
			_ = store.Close()
			return nil, "", err
		}
		return manager, backendSQLite, nil
	case backendPostgres, backendPostgresSQL:
		store, err := admin.NewPostgresConfigStore(dsn)
		if err != nil {
			return nil, "", err
		}
		manager, err := admin.NewGatewayConfigManager(gw, store)
		if err != nil {
			_ = store.Close()
			return nil, "", err
		}
		return manager, backendPostgres, nil
	default:
		return nil, "", fmt.Errorf("unsupported config store backend %q", backend)
	}
}
