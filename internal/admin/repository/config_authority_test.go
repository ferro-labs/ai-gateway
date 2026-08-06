package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/pkg/logger"
)

func baseConfig() config.Config {
	return config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "openai"}},
	}
}

// TestAdoptedStoredConfig reports whether the running config came from the
// store. Startup logs the resolved source from this answer, so it has to be
// exact: without it the boot log could only describe the file it read, which is
// how a persisted config with no plugins came to be announced as ten plugins
// loaded.
func TestAdoptedStoredConfig(t *testing.T) {
	t.Run("store holds a config", func(t *testing.T) {
		gw, err := newTestGateway(t, baseConfig())
		if err != nil {
			t.Fatalf("new gateway: %v", err)
		}
		persisted := baseConfig()
		persisted.Strategy.Mode = config.ModeFallback

		mgr, err := NewGatewayConfigManager(gw, &successConfigStore{cfg: persisted})
		if err != nil {
			t.Fatalf("new config manager: %v", err)
		}
		if !mgr.AdoptedStoredConfig() {
			t.Error("AdoptedStoredConfig() = false, want true — the store supplied the running config")
		}
		if got := gw.GetConfig().Strategy.Mode; got != config.ModeFallback {
			t.Errorf("running strategy = %q, want the persisted %q", got, config.ModeFallback)
		}
	})

	t.Run("store is empty", func(t *testing.T) {
		gw, err := newTestGateway(t, baseConfig())
		if err != nil {
			t.Fatalf("new gateway: %v", err)
		}
		mgr, err := NewGatewayConfigManager(gw, &successConfigStore{})
		if err != nil {
			t.Fatalf("new config manager: %v", err)
		}
		if mgr.AdoptedStoredConfig() {
			t.Error("AdoptedStoredConfig() = true, want false — nothing was persisted")
		}
	})

	t.Run("no store at all", func(t *testing.T) {
		gw, err := newTestGateway(t, baseConfig())
		if err != nil {
			t.Fatalf("new gateway: %v", err)
		}
		mgr, err := NewGatewayConfigManager(gw, nil)
		if err != nil {
			t.Fatalf("new config manager: %v", err)
		}
		if mgr.AdoptedStoredConfig() {
			t.Error("AdoptedStoredConfig() = true, want false — there is no store")
		}
	})
}

// TestReloadConfig_ObservabilityChangeReportsPendingRestart pins the one config
// section this process cannot adopt without a restart.
//
// internal/otel.Init runs once during startup, so a tracing change is stored
// and reported by GET /admin/config while the process keeps exporting exactly
// as it was. Accepting it silently would tell the operator a setting took
// effect that did not.
func TestReloadConfig_ObservabilityChangeReportsPendingRestart(t *testing.T) {
	gw, err := newTestGateway(t, baseConfig())
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	mgr, err := NewGatewayConfigManager(gw, &successConfigStore{})
	if err != nil {
		t.Fatalf("new config manager: %v", err)
	}

	enabled := true
	next := baseConfig()
	next.Observability.Tracing.Enabled = &enabled
	next.Observability.Tracing.Endpoint = "collector:4317"

	buf := captureLogger(t)
	if err := mgr.ReloadConfig(context.Background(), next); err != nil {
		t.Fatalf("ReloadConfig: %v", err)
	}

	entry := findLog(t, buf, "observability config accepted and persisted but not applied")
	if level, _ := entry["level"].(string); level != "WARN" {
		t.Errorf("level = %q, want WARN", level)
	}
	if entry["pending_restart"] != true {
		t.Errorf("pending_restart = %v, want true", entry["pending_restart"])
	}
}

// TestReloadConfig_LiveSectionsDoNotReportPendingRestart keeps the warning
// meaningful: strategy, targets, plugins, aliases, mcp_servers,
// request_timeout, compatibility and max_request_bytes are all re-read on
// reload, so none of them is pending anything.
func TestReloadConfig_LiveSectionsDoNotReportPendingRestart(t *testing.T) {
	gw, err := newTestGateway(t, baseConfig())
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	mgr, err := NewGatewayConfigManager(gw, &successConfigStore{})
	if err != nil {
		t.Fatalf("new config manager: %v", err)
	}

	next := baseConfig()
	next.Strategy.Mode = config.ModeFallback
	next.Targets = append(next.Targets, config.Target{VirtualKey: "anthropic"})
	next.MaxRequestBytes = 4096
	next.RequestTimeout = "30s"
	next.Aliases = map[string]string{"fast": "gpt-4o-mini"}
	next.Compatibility.OnUnsupportedParam = "drop"

	buf := captureLogger(t)
	if err := mgr.ReloadConfig(context.Background(), next); err != nil {
		t.Fatalf("ReloadConfig: %v", err)
	}

	if strings.Contains(buf.String(), "pending_restart") {
		t.Errorf("live-section changes must not report a pending restart:\n%s", buf.String())
	}
}

func captureLogger(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	previous := logger.Default()
	logger.SetDefault(logger.New(logger.Options{Level: "debug", Output: buf}))
	t.Cleanup(func() { logger.SetDefault(previous) })
	return buf
}

func findLog(t *testing.T, buf *bytes.Buffer, want string) map[string]any {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if msg, _ := entry["msg"].(string); strings.Contains(msg, want) {
			return entry
		}
	}
	t.Fatalf("no log line containing %q in:\n%s", want, buf.String())
	return nil
}
