package bootstrap

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/httpserver"
	"github.com/ferro-labs/ai-gateway/pkg/logger"
)

type orderedCloser struct {
	name  string
	order *[]string
}

func (c *orderedCloser) Close() error {
	*c.order = append(*c.order, c.name)
	return nil
}

func TestCloseRuntimeResourcesFlushesOTelLast(t *testing.T) {
	var order []string
	resources := []httpserver.NamedResource{
		{Name: "first", Value: &orderedCloser{name: "first", order: &order}},
		{Name: "second", Value: &orderedCloser{name: "second", order: &order}},
	}
	shutdown := func(context.Context) error {
		order = append(order, "otel")
		return nil
	}

	if err := closeRuntimeResources(resources, shutdown); err != nil {
		t.Fatalf("closeRuntimeResources returned an error: %v", err)
	}
	if want := []string{"first", "second", "otel"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("close order = %v, want %v", order, want)
	}
}

func TestServeAlreadyCanceledIsGraceful(t *testing.T) {
	t.Setenv("GATEWAY_CONFIG", "")
	t.Setenv("API_KEY_STORE_BACKEND", "memory")
	t.Setenv("CONFIG_STORE_BACKEND", "memory")
	t.Setenv("REQUEST_LOG_STORE_BACKEND", "")
	t.Setenv("PORT", "0")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := Serve(ctx); err != nil {
		t.Fatalf("Serve(already canceled) = %v, want graceful nil", err)
	}
}

func TestServeReturnsProductionSafetyErrors(t *testing.T) {
	t.Setenv("GATEWAY_ENV", "production")
	t.Setenv("ALLOW_UNAUTHENTICATED_PROXY", "true")

	if err := Serve(t.Context()); err == nil {
		t.Fatal("Serve must return production safety errors")
	}
}

func TestServeReturnsConfigErrors(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("strategy:\n  mode: not-a-mode\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GATEWAY_ENV", "")
	t.Setenv("ALLOW_UNAUTHENTICATED_PROXY", "")
	t.Setenv("GATEWAY_CONFIG", configPath)

	if err := Serve(t.Context()); err == nil {
		t.Fatal("Serve must return config errors")
	}
}

func TestBuildServerReturnsCompleteRuntime(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("strategy:\n  mode: single\ntargets:\n  - virtual_key: openai\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GATEWAY_CONFIG", configPath)
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("FERRO_MODEL_CATALOG_TIMEOUT", "0")
	t.Setenv("API_KEY_STORE_BACKEND", "memory")
	t.Setenv("CONFIG_STORE_BACKEND", "memory")
	t.Setenv("REQUEST_LOG_STORE_BACKEND", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	t.Setenv("PORT", "0")

	lg := logger.New(logger.FromEnv())
	app, err := buildServer(t.Context(), lg)
	if err != nil {
		t.Fatalf("buildServer: %v", err)
	}
	if app.gw == nil || app.srv == nil || app.cfgManager == nil || app.keyStore == nil || app.sessionStore == nil || app.auditStore == nil || app.otelShutdown == nil {
		t.Fatalf("buildServer returned an incomplete runtime: %+v", app)
	}
	if app.srv.Addr != ":0" {
		t.Errorf("server address = %q, want :0", app.srv.Addr)
	}
	t.Cleanup(func() {
		if err := closeRuntimeResources(app.resources(), app.otelShutdown); err != nil {
			t.Errorf("close runtime resources: %v", err)
		}
	})
}

func TestRunUntilShutdownReturnsListenErrors(t *testing.T) {
	srv := httpserver.NewServer("invalid::address", http.NotFoundHandler())

	if err := runUntilShutdown(t.Context(), nil, srv); err == nil {
		t.Fatal("runUntilShutdown must return listener errors")
	}
}

func TestGracefulShutdownWaitsForActiveHandlersBeforeClosingResources(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	handlerDone := make(chan struct{})
	var cleanupStarted atomic.Bool

	srv := httpserver.NewServer("127.0.0.1:0", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(started)
		<-release
		close(handlerDone)
	}))
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", srv.Addr)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(listener) }()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+listener.Addr().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		resp, requestErr := http.DefaultClient.Do(req)
		if requestErr == nil {
			_ = resp.Body.Close()
		}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("handler did not start")
	}

	app := &serverRuntime{
		srv: srv,
		otelShutdown: func(context.Context) error {
			cleanupStarted.Store(true)
			return nil
		},
	}
	done := make(chan error, 1)
	go func() { done <- gracefulShutdown(app, 10*time.Millisecond) }()

	select {
	case err := <-done:
		t.Fatalf("shutdown returned before the handler finished: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	if cleanupStarted.Load() {
		t.Fatal("cleanup started while the active handler was still running")
	}

	close(release)
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("handler did not finish")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("gracefulShutdown error = %v, want shutdown deadline error", err)
		}
	case <-time.After(time.Second):
		t.Fatal("gracefulShutdown did not return after the handler finished")
	}
	if !cleanupStarted.Load() {
		t.Fatal("cleanup did not run after the handler finished")
	}
}

func TestGracefulShutdownReturnsCleanupErrorsAndFlushesOTelLast(t *testing.T) {
	want := errors.New("cleanup failed")
	var order []string
	app := &serverRuntime{
		srv: httpserver.NewServer("", http.NotFoundHandler()),
		otelShutdown: func(context.Context) error {
			order = append(order, "otel")
			return want
		},
	}

	err := gracefulShutdown(app, time.Second)
	if !errors.Is(err, want) {
		t.Fatalf("gracefulShutdown error = %v, want cleanup error", err)
	}
	if wantOrder := []string{"otel"}; !reflect.DeepEqual(order, wantOrder) {
		t.Fatalf("close order = %v, want %v", order, wantOrder)
	}
}
