package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/ferro-labs/ai-gateway/mcp"
)

// A stdio MCP server is a command the gateway process executes. Letting the
// admin API introduce or change one turns an admin key into a shell on the
// host, so stdio servers are pinned to the boot-time file config: the API may
// keep or drop them, never add or alter them.
func TestGatewayConfigManager_ReloadConfig_RejectsStdioMCPChanges(t *testing.T) {
	pinned := mcp.ServerConfig{Name: "fs", Command: "mcp-fs", Args: []string{"--root", "/data"}, AllowedTools: []string{"read", "list"}}
	initial := singleConfig()
	initial.MCPServers = []mcp.ServerConfig{pinned}

	cases := []struct {
		name    string
		servers []mcp.ServerConfig
		wantErr bool
	}{
		{"unchanged stdio server is kept", []mcp.ServerConfig{pinned}, false},
		{"stdio server may be dropped", nil, false},
		{"http server may be added", []mcp.ServerConfig{pinned, {Name: "web", URL: "https://mcp.example.com/mcp"}}, false},
		{"new stdio server is rejected", []mcp.ServerConfig{pinned, {Name: "sh", Command: "/bin/sh", Args: []string{"-c", "id"}}}, true},
		{"changed command is rejected", []mcp.ServerConfig{{Name: "fs", Command: "/bin/sh", Args: pinned.Args}}, true},
		{"changed args are rejected", []mcp.ServerConfig{{Name: "fs", Command: "mcp-fs", Args: []string{"--root", "/"}}}, true},
		{"changed env is rejected", []mcp.ServerConfig{{Name: "fs", Command: "mcp-fs", Args: pinned.Args, Env: map[string]string{"LD_PRELOAD": "/x.so"}}}, true},
		// An empty allowlist exposes every discovered tool, so clearing or
		// widening it hands back the capability the pin removes. Narrowing is
		// an operator tightening a server and stays allowed.
		{"allowlist may be narrowed", []mcp.ServerConfig{{Name: "fs", Command: "mcp-fs", Args: pinned.Args, AllowedTools: []string{"read"}}}, false},
		{"cleared allowlist is rejected", []mcp.ServerConfig{{Name: "fs", Command: "mcp-fs", Args: pinned.Args}}, true},
		{"widened allowlist is rejected", []mcp.ServerConfig{{Name: "fs", Command: "mcp-fs", Args: pinned.Args, AllowedTools: []string{"read", "list", "write"}}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gw, err := newTestGateway(t, initial)
			if err != nil {
				t.Fatalf("new gateway: %v", err)
			}
			mgr, err := NewGatewayConfigManager(gw, &successConfigStore{})
			if err != nil {
				t.Fatalf("new config manager: %v", err)
			}
			next := singleConfig()
			next.MCPServers = tc.servers
			err = mgr.ReloadConfig(context.Background(), next)
			if tc.wantErr {
				if !errors.Is(err, ErrStdioMCPPinned) {
					t.Fatalf("err = %v, want ErrStdioMCPPinned", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("reload: %v", err)
			}
		})
	}
}

// A stored config that violates the pin was written through the admin API;
// adopting it at start-up would reopen the path the pin closes.
func TestNewGatewayConfigManager_RejectsPersistedStdioMCPChanges(t *testing.T) {
	gw, err := newTestGateway(t, singleConfig())
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	persisted := singleConfig()
	persisted.MCPServers = []mcp.ServerConfig{{Name: "sh", Command: "/bin/sh", Args: []string{"-c", "id"}}}
	if _, err := NewGatewayConfigManager(gw, &successConfigStore{cfg: persisted}); !errors.Is(err, ErrStdioMCPPinned) {
		t.Fatalf("err = %v, want ErrStdioMCPPinned", err)
	}
}
