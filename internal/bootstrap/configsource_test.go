package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/internal/admin/repository"
	"github.com/ferro-labs/ai-gateway/pkg/logger"
)

// memConfigStore is a repository.ConfigStore that keeps one config in memory,
// standing in for a durable store without needing a database.
type memConfigStore struct {
	cfg config.Config
	ok  bool
}

func (s *memConfigStore) Save(_ context.Context, cfg config.Config) error {
	s.cfg, s.ok = cfg, true
	return nil
}

func (s *memConfigStore) Load(context.Context) (config.Config, bool, error) {
	return s.cfg, s.ok, nil
}

func (s *memConfigStore) Delete(context.Context) error {
	s.cfg, s.ok = config.Config{}, false
	return nil
}

func captureDefaultLogger(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	previous := logger.Default()
	logger.SetDefault(logger.New(logger.Options{Level: "debug", Output: buf}))
	t.Cleanup(func() { logger.SetDefault(previous) })
	return buf
}

func logEntries(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("log line is not JSON: %q: %v", line, err)
		}
		out = append(out, entry)
	}
	return out
}

// findEntry returns the first log entry whose msg contains want.
func findEntry(t *testing.T, buf *bytes.Buffer, want string) map[string]any {
	t.Helper()
	for _, entry := range logEntries(t, buf) {
		if msg, _ := entry["msg"].(string); strings.Contains(msg, want) {
			return entry
		}
	}
	t.Fatalf("no log line containing %q in:\n%s", want, buf.String())
	return nil
}

func hasEntry(t *testing.T, buf *bytes.Buffer, want string) bool {
	t.Helper()
	for _, entry := range logEntries(t, buf) {
		if msg, _ := entry["msg"].(string); strings.Contains(msg, want) {
			return true
		}
	}
	return false
}

func fileConfig() config.Config {
	return config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeFallback},
		Targets:  []config.Target{{VirtualKey: "openai"}, {VirtualKey: "groq"}},
		Plugins: []config.PluginConfig{{
			Name:    "word-filter",
			Type:    "guardrail",
			Stage:   "before_request",
			Enabled: true,
			Config:  map[string]any{"blocked_words": []any{"secret"}},
		}},
	}
}

func storedConfig() config.Config {
	return config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "openai"}},
	}
}

func newGateway(t *testing.T, cfg config.Config) *aigateway.Gateway {
	t.Helper()
	gw, err := aigateway.New(cfg)
	if err != nil {
		t.Fatalf("aigateway.New: %v", err)
	}
	t.Cleanup(func() { _ = gw.Close() })
	return gw
}

// TestResolveActiveConfig_StoreWins_NamesSourceAndWarnsOnDivergence is the
// regression test for the boot log describing a config that lost.
//
// A durable store holding a config with no plugins supersedes the file at
// startup. Before this, that happened with no log line at all, so the boot log
// still read "plugins loaded count=1" and named the word-filter guardrail while
// the running gateway had none.
func TestResolveActiveConfig_StoreWins_NamesSourceAndWarnsOnDivergence(t *testing.T) {
	t.Setenv("GATEWAY_CONFIG", "/etc/ferrogw/config.yaml")

	fileCfg := fileConfig()
	gw := newGateway(t, fileCfg)
	store := &memConfigStore{cfg: storedConfig(), ok: true}

	manager, err := repository.NewGatewayConfigManager(gw, store)
	if err != nil {
		t.Fatalf("NewGatewayConfigManager: %v", err)
	}

	buf := captureDefaultLogger(t)
	active := ResolveActiveConfig(gw, manager, &fileCfg, "sqlite")

	if active.Strategy.Mode != config.ModeSingle {
		t.Errorf("active strategy = %q, want the stored %q", active.Strategy.Mode, config.ModeSingle)
	}

	resolved := findEntry(t, buf, "active config resolved")
	if got := resolved["source"]; got != sourceStore {
		t.Errorf("source = %v, want %q", got, sourceStore)
	}
	if got := resolved["strategy"]; got != string(config.ModeSingle) {
		t.Errorf("logged strategy = %v, want the active %q", got, config.ModeSingle)
	}
	if got := resolved["plugins"]; got != float64(0) {
		t.Errorf("logged plugins = %v, want 0 — the running gateway has none", got)
	}
	if got := resolved["targets"]; got != float64(1) {
		t.Errorf("logged targets = %v, want 1", got)
	}
	if got := resolved["config_store"]; got != "sqlite" {
		t.Errorf("config_store = %v, want sqlite", got)
	}

	diverged := findEntry(t, buf, "config file superseded")
	if level, _ := diverged["level"].(string); level != "WARN" {
		t.Errorf("divergence line level = %q, want WARN", level)
	}
	if got := diverged["plugins_dropped"]; got != "word-filter" {
		t.Errorf("plugins_dropped = %v, want word-filter — the guardrail that stopped running", got)
	}
	if got := diverged["file_strategy"]; got != string(config.ModeFallback) {
		t.Errorf("file_strategy = %v, want %q", got, config.ModeFallback)
	}
}

// TestResolveActiveConfig_StoreAgreesWithFile_NoWarning keeps the warning
// meaningful: a store that holds the same config the file does has superseded
// nothing an operator needs to know about.
func TestResolveActiveConfig_StoreAgreesWithFile_NoWarning(t *testing.T) {
	t.Setenv("GATEWAY_CONFIG", "/etc/ferrogw/config.yaml")

	// Plugin-free on both sides: what this pins is that identical configs are
	// silent, and building a real plugin instance is beside that point.
	fileCfg := storedConfig()
	gw := newGateway(t, fileCfg)
	store := &memConfigStore{cfg: fileCfg, ok: true}

	manager, err := repository.NewGatewayConfigManager(gw, store)
	if err != nil {
		t.Fatalf("NewGatewayConfigManager: %v", err)
	}

	buf := captureDefaultLogger(t)
	ResolveActiveConfig(gw, manager, &fileCfg, "sqlite")

	if entry := findEntry(t, buf, "active config resolved"); entry["source"] != sourceStore {
		t.Errorf("source = %v, want %q", entry["source"], sourceStore)
	}
	if hasEntry(t, buf, "config file superseded") {
		t.Errorf("identical configs must not warn:\n%s", buf.String())
	}
}

// TestResolveActiveConfig_FileWins names the file as the source when no stored
// config exists.
func TestResolveActiveConfig_FileWins(t *testing.T) {
	t.Setenv("GATEWAY_CONFIG", "/etc/ferrogw/config.yaml")

	fileCfg := fileConfig()
	gw := newGateway(t, fileCfg)

	manager, err := repository.NewGatewayConfigManager(gw, &memConfigStore{})
	if err != nil {
		t.Fatalf("NewGatewayConfigManager: %v", err)
	}

	buf := captureDefaultLogger(t)
	ResolveActiveConfig(gw, manager, &fileCfg, "sqlite")

	entry := findEntry(t, buf, "active config resolved")
	if entry["source"] != sourceFile {
		t.Errorf("source = %v, want %q", entry["source"], sourceFile)
	}
	if entry["file"] != "/etc/ferrogw/config.yaml" {
		t.Errorf("file = %v, want the GATEWAY_CONFIG path", entry["file"])
	}
	if entry["plugins"] != float64(1) {
		t.Errorf("plugins = %v, want 1", entry["plugins"])
	}
	if hasEntry(t, buf, "config file superseded") {
		t.Errorf("no store config means no divergence:\n%s", buf.String())
	}
}

// TestResolveActiveConfig_DefaultsWin names defaults when neither a file nor a
// stored config supplied anything.
func TestResolveActiveConfig_DefaultsWin(t *testing.T) {
	t.Setenv("GATEWAY_CONFIG", "")

	gw := newGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeFallback},
		Targets:  []config.Target{{VirtualKey: "openai"}},
	})
	manager, err := repository.NewGatewayConfigManager(gw, nil)
	if err != nil {
		t.Fatalf("NewGatewayConfigManager: %v", err)
	}

	buf := captureDefaultLogger(t)
	ResolveActiveConfig(gw, manager, nil, BackendMemory)

	entry := findEntry(t, buf, "active config resolved")
	if entry["source"] != sourceDefaults {
		t.Errorf("source = %v, want %q", entry["source"], sourceDefaults)
	}
	if _, ok := entry["file"]; ok {
		t.Errorf("no GATEWAY_CONFIG means no file attribute: %v", entry)
	}
	if entry["max_request_bytes"] != float64(config.DefaultMaxRequestBytes) {
		t.Errorf("max_request_bytes = %v, want the enforced default %d",
			entry["max_request_bytes"], config.DefaultMaxRequestBytes)
	}
}

// TestWarnUnreadConfigFile reports a config file mounted at the conventional
// container path while GATEWAY_CONFIG is unset — the shape that makes a compose
// bind mount silently do nothing.
func TestWarnUnreadConfigFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("strategy:\n  mode: single\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	previous := wellKnownConfigPaths
	wellKnownConfigPaths = []string{path}
	t.Cleanup(func() { wellKnownConfigPaths = previous })

	t.Setenv("GATEWAY_CONFIG", "")
	buf := captureDefaultLogger(t)

	if cfg, err := LoadConfig(); err != nil || cfg != nil {
		t.Fatalf("LoadConfig must not adopt an unnamed file, got %+v", cfg)
	}

	entry := findEntry(t, buf, "GATEWAY_CONFIG is unset")
	if level, _ := entry["level"].(string); level != "WARN" {
		t.Errorf("level = %q, want WARN", level)
	}
	if entry["path"] != path {
		t.Errorf("path = %v, want %q", entry["path"], path)
	}
}

// TestWarnUnreadConfigFile_SilentWhenNamed keeps the warning to the case it is
// about: an operator who set GATEWAY_CONFIG does not need to be told the file
// exists.
func TestWarnUnreadConfigFile_SilentWhenNamed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("strategy:\n  mode: single\ntargets:\n  - virtual_key: openai\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	previous := wellKnownConfigPaths
	wellKnownConfigPaths = []string{path}
	t.Cleanup(func() { wellKnownConfigPaths = previous })

	t.Setenv("GATEWAY_CONFIG", path)
	buf := captureDefaultLogger(t)

	if cfg, err := LoadConfig(); err != nil || cfg == nil {
		t.Fatal("LoadConfig returned nil for a named, valid config file")
	}
	if hasEntry(t, buf, "GATEWAY_CONFIG is unset") {
		t.Errorf("named config must not warn about being unread:\n%s", buf.String())
	}
	if entry := findEntry(t, buf, "config file loaded"); entry["path"] != path {
		t.Errorf("config file loaded path = %v, want %q", entry["path"], path)
	}
}
