package run

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// isolateStores keeps a developer's shell from steering the boot under test at
// a real backend: with either backend set to sqlite, the empty-DSN default
// writes ferrogw-*.db into this package directory.
func isolateStores(t *testing.T) {
	t.Helper()
	t.Setenv("GATEWAY_ENV", "")
	t.Setenv("ALLOW_UNAUTHENTICATED_PROXY", "")
	t.Setenv("REQUEST_LOG_STORE_BACKEND", "")
	t.Setenv("API_KEY_STORE_BACKEND", "")
	t.Setenv("CONFIG_STORE_BACKEND", "")
}

func TestRunReturnsStartupErrors(t *testing.T) {
	t.Setenv("GATEWAY_ENV", "production")
	t.Setenv("ALLOW_UNAUTHENTICATED_PROXY", "true")

	err := Run(t.Context())
	if err == nil {
		t.Fatal("Run must return startup errors instead of terminating the process")
	}
}

// A failure inside buildServer — here an invalid config file — used to exit 1
// after logging. The error return must arrive as an error, not as the panic a
// typed-nil *Gateway produced when the cleanup path tried to Close it.
func TestRunReturnsConfigErrorsWithoutPanicking(t *testing.T) {
	isolateStores(t)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("strategy:\n  mode: not-a-mode\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GATEWAY_CONFIG", configPath)

	err := Run(t.Context())
	if err == nil {
		t.Fatal("Run must return the config error")
	}
}

func TestRunHonorsCanceledContext(t *testing.T) {
	isolateStores(t)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("strategy:\n  mode: single\ntargets:\n  - virtual_key: openai\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GATEWAY_CONFIG", configPath)
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("FERRO_MODEL_CATALOG_TIMEOUT", "0")
	t.Setenv("PORT", "0")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	done := make(chan error, 1)
	go func() { done <- Run(ctx) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned an error for context cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not trigger graceful shutdown after context cancellation")
	}
}
