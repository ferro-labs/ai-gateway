package mcp

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// TestNewStdioClientInvalidCommand verifies that starting a non-existent
// executable returns an errClient whose Initialize method surfaces the error.
func TestNewStdioClientInvalidCommand(t *testing.T) {
	c := newStdioClient("test", "/nonexistent/binary-that-does-not-exist", nil, nil)
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

// realCmd returns a command guaranteed to exist on the current platform, or
// skips the test if none is found. The command exits immediately without
// speaking the MCP protocol, so all stdioClient methods must return errors.
func realCmd(t *testing.T) string {
	t.Helper()
	for _, p := range []string{"/usr/bin/true", "/bin/true"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Skip("no suitable real command found for stdio tests on this platform")
	return ""
}

// TestStdioClientHappyPathCreation verifies that newStdioClient returns a
// *stdioClient (not an errClient) when the command exists on disk.
func TestStdioClientHappyPathCreation(t *testing.T) {
	cmd := realCmd(t)
	c := newStdioClient("test", cmd, nil, nil)
	if c == nil {
		t.Fatal("expected non-nil client")
	}
	if _, isErr := c.(*errClient); isErr {
		t.Fatal("expected stdioClient, got errClient for valid command")
	}
	_ = c.Close()
}

// TestStdioClientEnvOverrides verifies that env overrides are merged without
// panic. Uses an invalid command so no subprocess is actually started.
func TestStdioClientEnvOverrides(t *testing.T) {
	c := newStdioClient("test", "/nonexistent/mcp-srv", nil, map[string]string{
		"CUSTOM_KEY": "custom_val",
		"OTHER_KEY":  "other_val",
	})
	if c == nil {
		t.Fatal("expected non-nil client")
	}
	_, err := c.Initialize(context.Background())
	if err == nil {
		t.Fatal("expected error from errClient Initialize")
	}
}

// TestStdioClientInitializeError verifies that stdioClient.Initialize returns
// a non-nil error when the subprocess exits without sending an MCP response.
func TestStdioClientInitializeError(t *testing.T) {
	cmd := realCmd(t)
	c := newStdioClient("test", cmd, nil, nil)
	if _, isErr := c.(*errClient); isErr {
		t.Skip("command failed to start — skipping stdioClient path")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := c.Initialize(ctx)
	if err == nil {
		t.Fatal("expected error initializing with non-MCP subprocess")
	}
	_ = c.Close()
}

// TestStdioClientListToolsError verifies that stdioClient.ListTools returns a
// non-nil error when the subprocess has not completed MCP initialization.
func TestStdioClientListToolsError(t *testing.T) {
	cmd := realCmd(t)
	c := newStdioClient("test", cmd, nil, nil)
	if _, isErr := c.(*errClient); isErr {
		t.Skip("command failed to start — skipping stdioClient path")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := c.ListTools(ctx)
	if err == nil {
		t.Fatal("expected error calling ListTools on non-MCP subprocess")
	}
	_ = c.Close()
}

// TestStdioClientCallToolError verifies that stdioClient.CallTool returns a
// non-nil error when the subprocess has not completed MCP initialization.
func TestStdioClientCallToolError(t *testing.T) {
	cmd := realCmd(t)
	c := newStdioClient("test", cmd, nil, nil)
	if _, isErr := c.(*errClient); isErr {
		t.Skip("command failed to start — skipping stdioClient path")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := c.CallTool(ctx, "test-tool", json.RawMessage(`{"key":"val"}`))
	if err == nil {
		t.Fatal("expected error calling CallTool on non-MCP subprocess")
	}
	_ = c.Close()
}

// TestStdioClientCloseAfterError verifies that Close does not panic on a
// stdioClient whose subprocess exited before any MCP handshake.
func TestStdioClientCloseAfterError(t *testing.T) {
	cmd := realCmd(t)
	c := newStdioClient("test", cmd, nil, nil)
	if _, isErr := c.(*errClient); isErr {
		t.Skip("command failed to start — skipping stdioClient path")
	}
	if err := c.Close(); err != nil {
		// Some transports return an error on close after subprocess exit; that
		// is acceptable as long as Close does not panic.
		t.Logf("Close returned (acceptable) error: %v", err)
	}
}

// TestRegistryReregistration verifies that re-registering a server with the
// same name preserves registration order and does not duplicate entries.
func TestRegistryReregistration(t *testing.T) {
	reg := NewRegistry()
	cfg := ServerConfig{
		Name:    "reregister-srv",
		Command: "/nonexistent/mcp-server",
	}
	reg.RegisterConfig(cfg)
	reg.RegisterConfig(cfg) // second registration of the same name

	names := reg.ServerNames()
	if len(names) != 1 {
		t.Fatalf("expected 1 server after re-registration, got %d", len(names))
	}
	if names[0] != "reregister-srv" {
		t.Fatalf("unexpected server name: %s", names[0])
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
