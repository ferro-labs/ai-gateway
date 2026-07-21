package aigateway

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/mcp"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// mcpUndefinedVar names the environment variable the malformed-server fixtures
// below reference. Their whole premise is that it does NOT resolve, so the
// fixtures must not depend on it merely happening to be absent from the ambient
// environment: an exported value would make the "broken" server register and
// invert what every one of these tests asserts.
const mcpUndefinedVar = "GATEWAY_MCP_TEST_UNDEFINED_VAR"

// requireUnsetEnv guarantees key is unset for the duration of the test and
// restores any prior value afterwards.
func requireUnsetEnv(t *testing.T, key string) {
	t.Helper()
	prev, had := os.LookupEnv(key)
	if !had {
		return
	}
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unset %s: %v", key, err)
	}
	t.Cleanup(func() {
		if err := os.Setenv(key, prev); err != nil {
			t.Errorf("restore %s: %v", key, err)
		}
	})
}

// TestWireMCPLocked_MalformedServerDoesNotBlockOthers verifies that one MCP
// server with an unresolvable ${VAR} in its headers does not prevent
// subsequent, well-formed servers in the same config from being registered.
func TestWireMCPLocked_MalformedServerDoesNotBlockOthers(t *testing.T) {
	requireUnsetEnv(t, mcpUndefinedVar)
	gw := &Gateway{}
	ctx, cancel := context.WithCancel(context.Background())
	gw.shutdownCtx = ctx
	gw.shutdownCancel = cancel
	t.Cleanup(cancel)

	cfg := Config{
		MCPServers: []mcp.ServerConfig{
			{
				Name: "broken",
				URL:  "http://127.0.0.1:1/mcp",
				Headers: map[string]string{
					"Authorization": "Bearer ${" + mcpUndefinedVar + "}",
				},
			},
			{
				Name: "good",
				URL:  "http://127.0.0.1:1/mcp",
			},
		},
	}

	gw.wireMCPLocked(cfg, "test: mcp init failed")

	if gw.mcpRegistry == nil {
		t.Fatal("mcpRegistry is nil, want a registry containing the well-formed server")
	}
	names := gw.mcpRegistry.ServerNames()
	if !slices.Contains(names, "good") {
		t.Errorf("ServerNames() = %v, want it to contain %q", names, "good")
	}

	// The broken server is recorded rather than dropped: readiness reads the
	// registry, so a server missing from it cannot be reported at all. It is
	// present and unready, with its reason retained.
	if !slices.Contains(names, "broken") {
		t.Fatalf("ServerNames() = %v, want it to contain %q so its failure stays visible", names, "broken")
	}
	if gw.mcpRegistry.IsReady("broken") {
		t.Error("a server whose headers never resolved is reported ready")
	}
	for _, st := range gw.mcpRegistry.Status() {
		if st.Name == "broken" && st.LastError == "" {
			t.Error("Status() lost the reason the server could not be built")
		}
	}
}

// TestReloadConfig_MalformedMCPServerDoesNotLeaveStaleRegistry verifies that a
// reload whose new config contains one malformed MCP server still rebuilds the
// registry from the new config, rather than leaving the pre-reload registry in
// place.
func TestReloadConfig_MalformedMCPServerDoesNotLeaveStaleRegistry(t *testing.T) {
	requireUnsetEnv(t, mcpUndefinedVar)
	gw := &Gateway{}
	ctx, cancel := context.WithCancel(context.Background())
	gw.shutdownCtx = ctx
	gw.shutdownCancel = cancel
	t.Cleanup(cancel)

	base := Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "test-provider"}},
	}

	initial := base
	initial.MCPServers = []mcp.ServerConfig{{Name: "old-only", URL: "http://127.0.0.1:1/mcp"}}
	gw.wireMCPLocked(initial, "test: initial mcp init failed")

	reloaded := base
	reloaded.MCPServers = []mcp.ServerConfig{
		{
			Name: "broken",
			URL:  "http://127.0.0.1:1/mcp",
			Headers: map[string]string{
				"Authorization": "Bearer ${" + mcpUndefinedVar + "}",
			},
		},
		{Name: "good2", URL: "http://127.0.0.1:1/mcp"},
	}

	if err := gw.ReloadConfig(context.Background(), reloaded); err != nil {
		t.Fatalf("ReloadConfig() error = %v", err)
	}

	gw.mu.RLock()
	reg := gw.mcpRegistry
	gw.mu.RUnlock()
	if reg == nil {
		t.Fatal("mcpRegistry is nil after reload, want the rebuilt registry")
	}
	names := reg.ServerNames()
	if slices.Contains(names, "old-only") {
		t.Errorf("ServerNames() = %v, still contains the pre-reload server; registry was not rebuilt from the new config", names)
	}
	if !slices.Contains(names, "good2") {
		t.Errorf("ServerNames() = %v, want it to contain %q from the reloaded config", names, "good2")
	}
}

// TestWireMCPLocked_MaxCallDepthIgnoresSkippedServers verifies that a server
// skipped due to unresolvable headers does not contribute its MaxCallDepth to
// the shared executor's depth limit.
func TestWireMCPLocked_MaxCallDepthIgnoresSkippedServers(t *testing.T) {
	requireUnsetEnv(t, mcpUndefinedVar)
	gw := &Gateway{}
	ctx, cancel := context.WithCancel(context.Background())
	gw.shutdownCtx = ctx
	gw.shutdownCancel = cancel
	t.Cleanup(cancel)

	// The good server must actually serve tools: ShouldContinueLoop only arms
	// for tool calls MCP owns, so a registry that discovered nothing can no
	// longer stand in for a healthy one.
	goodSrv := newMCPTestServer(t)
	defer goodSrv.Close()

	cfg := Config{
		MCPServers: []mcp.ServerConfig{
			{
				Name:         "broken",
				URL:          "http://127.0.0.1:1/mcp",
				MaxCallDepth: 1,
				Headers: map[string]string{
					"Authorization": "Bearer ${" + mcpUndefinedVar + "}",
				},
			},
			{
				Name:         "good",
				URL:          goodSrv.URL,
				MaxCallDepth: 5,
			},
		},
	}

	gw.wireMCPLocked(cfg, "test: mcp init failed")

	if gw.mcpExecutor == nil {
		t.Fatal("mcpExecutor is nil, want an executor built from the well-formed server")
	}

	select {
	case <-gw.MCPInitDone():
	case <-time.After(10 * time.Second):
		t.Fatal("MCP init timeout")
	}

	resp := &core.Response{
		Choices: []core.Choice{
			{Message: core.Message{ToolCalls: []core.ToolCall{{ID: "1", Function: core.FunctionCall{Name: "get_answer"}}}}},
		},
	}

	// The broken server's max_call_depth: 1 must not clamp the shared
	// executor's depth limit down from the good server's max_call_depth: 5.
	if !gw.mcpExecutor.ShouldContinueLoop(resp, 4) {
		t.Error("ShouldContinueLoop(resp, 4) = false, want true: depth limit should be 5 (the good server's), not 1 (the skipped server's)")
	}
}
