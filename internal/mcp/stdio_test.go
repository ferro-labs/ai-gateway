package mcp

import (
	"context"
	"encoding/json"
	"testing"
)

// TestNewStdioClientInvalidCommand verifies that starting a non-existent
// executable returns an errClient whose Initialize method surfaces the error.
func TestNewStdioClientInvalidCommand(t *testing.T) {
	c := newStdioClient("/nonexistent/binary-that-does-not-exist", nil, nil)
	if c == nil {
		t.Fatal("expected non-nil client")
	}

	_, err := c.Initialize(context.Background())
	if err == nil {
		t.Fatal("expected error from Initialize on bad command, got nil")
	}

	_, err = c.ListTools(context.Background())
	if err == nil {
		t.Fatal("expected error from ListTools on bad command, got nil")
	}

	_, err = c.CallTool(context.Background(), "any", json.RawMessage("{}"))
	if err == nil {
		t.Fatal("expected error from CallTool on bad command, got nil")
	}

	if err := c.Close(); err != nil {
		t.Fatalf("errClient.Close() unexpected error: %v", err)
	}
}

// TestRegistryStdioConfigDispatch verifies that a ServerConfig with a Command
// field creates a stdio client (not an HTTP client) in the registry.
// It uses an invalid command so no subprocess actually starts.
func TestRegistryStdioConfigDispatch(t *testing.T) {
	reg := NewRegistry()
	reg.RegisterConfig(ServerConfig{
		Name:    "stdio-srv",
		Command: "/nonexistent/mcp-server",
		Args:    []string{"--stdio"},
	})

	if !reg.HasServers() {
		t.Fatal("expected HasServers true")
	}

	var initErr error
	reg.InitializeAll(context.Background(), func(_ string, err error) {
		initErr = err
	})

	// The server should fail to initialize (bad command), not panic.
	if initErr == nil {
		t.Fatal("expected initialization error for non-existent command")
	}
	if reg.IsReady("stdio-srv") {
		t.Fatal("expected server not ready after failed init")
	}
}

// TestRegistryStdioClose verifies that Registry.Close() does not panic when
// stdio clients are registered (even if they failed to start).
func TestRegistryStdioClose(t *testing.T) {
	reg := NewRegistry()
	reg.RegisterConfig(ServerConfig{
		Name:    "stdio-close",
		Command: "/nonexistent/mcp-server",
	})
	// Close must not panic regardless of client state.
	if err := reg.Close(); err != nil {
		// errClient.Close() returns nil, so this is unexpected.
		t.Fatalf("Registry.Close() unexpected error: %v", err)
	}
}
