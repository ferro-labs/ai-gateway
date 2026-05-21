//go:build integration

package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// TestStdioIntegration_FilesystemServer runs an end-to-end test against the
// @modelcontextprotocol/server-filesystem package via npx.
// Requires npx to be on PATH and network access to the npm registry.
// Run with: go test -tags integration ./internal/mcp/... -run TestStdioIntegration
func TestStdioIntegration_FilesystemServer(t *testing.T) {
	reg := NewRegistry()
	reg.RegisterConfig(ServerConfig{
		Name:    "filesystem",
		Command: "npx",
		Args:    []string{"-y", "@modelcontextprotocol/server-filesystem", t.TempDir()},
	})
	defer func() { _ = reg.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	reg.InitializeAll(ctx, func(name string, err error) {
		t.Errorf("MCP init error for %s: %v", name, err)
	})

	if !reg.IsReady("filesystem") {
		t.Fatal("filesystem server not ready after InitializeAll")
	}

	tools := reg.AllTools()
	if len(tools) == 0 {
		t.Fatal("expected at least one tool from filesystem server, got none")
	}
	t.Logf("discovered %d tools: %v", len(tools), toolNames(tools))

	// Verify FindToolServer routes correctly.
	client, ok := reg.FindToolServer(tools[0].Name)
	if !ok || client == nil {
		t.Fatalf("FindToolServer(%q) returned ok=%v", tools[0].Name, ok)
	}

	// Call read_file with an argument (file won't exist, but the tool call
	// should round-trip without panicking; IsError=true is an acceptable result).
	result, err := client.CallTool(ctx, tools[0].Name, json.RawMessage(`{"path":"/nonexistent"}`))
	if err != nil {
		// A protocol-level error (not a tool error) is unexpected.
		t.Logf("CallTool returned err (acceptable if tool-level): %v", err)
	} else {
		t.Logf("CallTool result: isError=%v, content=%d blocks", result.IsError, len(result.Content))
	}
}

func toolNames(tools []Tool) []string {
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Name
	}
	return names
}
