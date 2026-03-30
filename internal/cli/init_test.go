package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateMasterKey(t *testing.T) {
	key := GenerateMasterKey()
	if !strings.HasPrefix(key, "fgw_") {
		t.Errorf("master key should have fgw_ prefix, got %q", key)
	}
	// fgw_ (4) + 32 hex chars = 36 total
	if len(key) != 36 {
		t.Errorf("master key should be 36 chars, got %d", len(key))
	}

	// Uniqueness.
	key2 := GenerateMasterKey()
	if key == key2 {
		t.Error("two generated keys should not be equal")
	}
}

func TestWriteConfigYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	err := WriteDefaultConfig(path, "yaml")
	if err != nil {
		t.Fatalf("WriteDefaultConfig: %v", err)
	}

	data, err := os.ReadFile(path) //nolint:gosec // path is from t.TempDir()
	if err != nil {
		t.Fatalf("read config: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "mode: fallback") {
		t.Error("config should contain fallback strategy")
	}
	if !strings.Contains(content, "virtual_key: openai") {
		t.Error("config should contain openai target")
	}
}

func TestWriteConfigJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	err := WriteDefaultConfig(path, "json")
	if err != nil {
		t.Fatalf("WriteDefaultConfig: %v", err)
	}

	data, err := os.ReadFile(path) //nolint:gosec // path is from t.TempDir()
	if err != nil {
		t.Fatalf("read config: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "fallback") {
		t.Error("config should contain fallback strategy")
	}
}

func TestWriteConfigNoOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	// Create existing file.
	_ = os.WriteFile(path, []byte("existing"), 0600)

	err := WriteDefaultConfig(path, "yaml")
	if err == nil {
		t.Error("should error when file already exists")
	}
}
