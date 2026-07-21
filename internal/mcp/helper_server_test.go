package mcp

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

// The stdio death-detection tests need a subprocess that really speaks MCP:
// stderr EOF is only distinguishable from death by asking the server, and a
// server that cannot answer an initialize or a ping proves nothing either way.
// Re-executing this test binary in server mode is the portable way to get one
// without depending on npx, python, or a shell.
const (
	// helperModeEnv puts the re-executed binary into MCP-server mode. Absent
	// for the ordinary test run, which is how TestHelperMCPServer knows to skip.
	helperModeEnv = "FERRO_MCP_HELPER_MODE"

	// helperModeServe starts the server with stderr left open, the normal case.
	helperModeServe = "serve"
	// helperModeStderrClosed closes stderr before the handshake, modelling a
	// server that gives up its error stream at startup. The gateway sees EOF
	// before the server is ever ready.
	helperModeStderrClosed = "stderr-closed"
)

// Tools the helper advertises. Two of them exist to drive the server's own
// lifecycle from the test, since that is the only way to sequence "close
// stderr" and "die" against a completed handshake.
const (
	helperToolEcho        = "helper_echo"
	helperToolCloseStderr = "helper_close_stderr"
	helperToolDie         = "helper_die"
)

// helperServerConfig returns a ServerConfig that launches this test binary as a
// real MCP stdio server. newStdioClient gives the subprocess a minimal
// environment plus Env, so the mode switch travels there.
func helperServerConfig(t *testing.T, name, mode string) ServerConfig {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("cannot locate the test binary to re-execute as an MCP server: %v", err)
	}
	return ServerConfig{
		Name:    name,
		Command: exe,
		Args:    []string{"-test.run=^TestHelperMCPServer$"},
		Env:     map[string]string{helperModeEnv: mode},
	}
}

// TestHelperMCPServer is the subprocess entry point, not a test. It skips
// unless helperModeEnv selected server mode.
func TestHelperMCPServer(t *testing.T) {
	mode := os.Getenv(helperModeEnv)
	if mode == "" {
		t.Skip("not running as the MCP helper subprocess")
	}
	runHelperMCPServer(mode)
}

// runHelperMCPServer serves newline-delimited JSON-RPC on stdin/stdout until
// stdin closes, then exits without returning to the test framework — anything
// it printed would land in the middle of the protocol stream.
func runHelperMCPServer(mode string) {
	if mode == helperModeStderrClosed {
		_ = os.Stderr.Close()
	}

	dec := json.NewDecoder(bufio.NewReader(os.Stdin))
	out := bufio.NewWriter(os.Stdout)
	for {
		var req struct {
			ID     *json.RawMessage `json:"id"`
			Method string           `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		if err := dec.Decode(&req); err != nil {
			os.Exit(0)
		}
		if req.ID == nil {
			// A notification (notifications/initialized); nothing to answer.
			continue
		}

		// Death is modelled as an exit with the request unanswered, which is what
		// a crash looks like from the gateway's side.
		if req.Method == "tools/call" && req.Params.Name == helperToolDie {
			os.Exit(1)
		}

		if err := writeHelperResponse(out, *req.ID, helperResult(req.Method)); err != nil {
			os.Exit(1)
		}

		// After the reply, so the caller's tool call completes before the pipe
		// goes away and the test can sequence on it.
		if req.Method == "tools/call" && req.Params.Name == helperToolCloseStderr {
			_ = os.Stderr.Close()
		}
	}
}

// helperResult builds the result payload for one MCP method.
func helperResult(method string) any {
	switch method {
	case "initialize":
		return map[string]any{
			// Echoing the client's latest version keeps the handshake valid
			// without pinning the test to a specific revision of the spec.
			"protocolVersion": mcpgo.LATEST_PROTOCOL_VERSION,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]string{"name": "ferro-helper", "version": "0"},
		}
	case "tools/list":
		schema := map[string]any{"type": "object"}
		return map[string]any{"tools": []map[string]any{
			{"name": helperToolEcho, "description": "echo", "inputSchema": schema},
			{"name": helperToolCloseStderr, "description": "close stderr", "inputSchema": schema},
			{"name": helperToolDie, "description": "exit", "inputSchema": schema},
		}}
	case "tools/call":
		return map[string]any{
			"content": []map[string]any{{"type": "text", "text": "ok"}},
			"isError": false,
		}
	default:
		// Covers ping, whose result is an empty object.
		return map[string]any{}
	}
}

// writeHelperResponse emits one JSON-RPC response frame and flushes it, since
// the peer is blocked reading a single line.
func writeHelperResponse(out *bufio.Writer, id json.RawMessage, result any) error {
	resp := struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  any             `json:"result"`
	}{JSONRPC: "2.0", ID: id, Result: result}

	if err := json.NewEncoder(out).Encode(resp); err != nil {
		return err
	}
	return out.Flush()
}

// initHelperServer registers the helper under name, runs the real handshake,
// and returns the registry with the server ready.
func initHelperServer(t *testing.T, name, mode string) *Registry {
	t.Helper()
	reg := NewRegistry()
	t.Cleanup(func() { _ = reg.Close() })

	reg.RegisterConfig(helperServerConfig(t, name, mode))
	reg.InitializeAll(t.Context(), func(n string, err error) {
		t.Errorf("helper server %s failed to initialize: %v", n, err)
	})
	if !reg.IsReady(name) {
		t.Fatalf("helper server %q did not become ready; the subprocess is not speaking MCP", name)
	}
	return reg
}

// helperClient returns the live transport for name.
func helperClient(t *testing.T, reg *Registry, name string) mcpClient {
	t.Helper()
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	entry, ok := reg.servers[name]
	if !ok {
		t.Fatalf("server %q is not registered", name)
	}
	return entry.client
}
