//go:build integration
// +build integration

// Package http_test provides integration tests for the full wired HTTP router.
// Tests boot the gateway via the same bootstrap path as production code
// (httpserver.NewRouter) but substitute a stub provider so no real LLM calls
// are made.
package http_test

import (
	"context"
	"net/http/httptest"
	"os"
	"testing"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/internal/admin"
	"github.com/ferro-labs/ai-gateway/internal/httpserver"
	"github.com/ferro-labs/ai-gateway/internal/ratelimit"
	"github.com/ferro-labs/ai-gateway/internal/requestlog"
	"github.com/ferro-labs/ai-gateway/providers"
)

const (
	testMasterKey  = "test-master-key-for-integration"
	stubModelName  = "stub-model-v1"
	stubModelName2 = "stub-model-v2"
	stubEmbedModel = "stub-embed-v1"
)

// testEnv holds a fully wired test server and its dependencies.
type testEnv struct {
	Server   *httptest.Server
	Gateway  *aigateway.Gateway
	Registry *providers.Registry
	KeyStore admin.Store
	Stub     *stubProvider
}

// newTestServer creates an httptest.Server wired exactly like production via
// httpserver.NewRouter. It registers a single stub provider.
// Callers should defer env.Server.Close() in their tests.
func newTestServer(t *testing.T, opts ...testOption) *testEnv {
	t.Helper()

	cfg := testConfig{
		corsOrigins: nil,
		rlStore:     nil,
	}
	for _, o := range opts {
		o(&cfg)
	}

	// Ensure ALLOW_UNAUTHENTICATED_PROXY is not set so auth middleware is active.
	t.Setenv("ALLOW_UNAUTHENTICATED_PROXY", "false")

	stub := newStubProvider("stub", []string{stubModelName, stubModelName2, stubEmbedModel})

	registry := providers.NewRegistry()
	registry.Register(stub)

	gwCfg := aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeFallback},
		Targets:  []aigateway.Target{{VirtualKey: "stub"}},
	}
	gw, err := aigateway.New(gwCfg)
	if err != nil {
		t.Fatalf("newTestServer: create gateway: %v", err)
	}
	gw.RegisterProvider(stub)

	keyStore := admin.NewKeyStore()

	router := httpserver.NewRouter(
		registry,
		keyStore,
		cfg.corsOrigins,
		gw,
		nil, // cfgManager — not needed for these tests
		cfg.rlStore,
		noopReader{},
		noopMaintainer{},
		testMasterKey,
	)

	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	return &testEnv{
		Server:   srv,
		Gateway:  gw,
		Registry: registry,
		KeyStore: keyStore,
		Stub:     stub,
	}
}

type testConfig struct {
	corsOrigins []string
	rlStore     *ratelimit.Store
}

type testOption func(*testConfig)

func withCORSOrigins(origins ...string) testOption {
	return func(c *testConfig) { c.corsOrigins = origins }
}

func withRateLimit(rps, burst float64) testOption {
	return func(c *testConfig) { c.rlStore = ratelimit.NewStore(rps, burst) }
}

// noopReader satisfies requestlog.Reader without a database.
type noopReader struct{}

func (noopReader) List(_ context.Context, _ requestlog.Query) (requestlog.ListResult, error) {
	return requestlog.ListResult{}, nil
}

// noopMaintainer satisfies requestlog.Maintainer without a database.
type noopMaintainer struct{}

func (noopMaintainer) Delete(_ context.Context, _ requestlog.MaintenanceQuery) (int, error) {
	return 0, nil
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
