package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client/transport"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// toolCallNamed builds the minimal tool call the executor needs.
func toolCallNamed(name string) core.ToolCall {
	return core.ToolCall{
		ID:       "tc-" + name,
		Type:     "function",
		Function: core.FunctionCall{Name: name, Arguments: "{}"},
	}
}

// waitUntil polls cond until it holds or the deadline expires, failing with
// desc on timeout. Child death is observed by a goroutine draining the
// subprocess's stderr, so the flip is asynchronous by construction and there is
// no event to synchronise on. Distinct from the unix-only waitFor in
// proc_unix_test.go so this file builds on every platform.
func waitUntil(t *testing.T, timeout time.Duration, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, desc)
}

// shortLivedCmd returns a command that exists, starts, and exits immediately.
// The exit is the point: it models an MCP server whose process dies while the
// gateway still holds it registered and ready.
func shortLivedCmd(t *testing.T) string {
	t.Helper()
	for _, p := range []string{"/usr/bin/true", "/bin/true"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Skip("no suitable real command found for stdio tests on this platform")
	return ""
}

// TestStdioChildDeathMarksServerUnready is the regression test for a crashed
// stdio subprocess going undetected.
//
// Before the fix nothing ever cleared serverEntry.ready outside Close, so a
// dead child kept its tools advertised by AllTools and kept being resolved by
// FindToolServer — every call then failed with "transport closed" while the
// model was still being told the tools were available.
//
// The child here is spawned for real and exits on its own. The registry entry
// is then hand-marked ready with its discovered tool, which is the state a
// successful handshake would have left behind had the process lived longer.
func TestStdioChildDeathMarksServerUnready(t *testing.T) {
	cmd := shortLivedCmd(t)

	reg := NewRegistry()
	defer func() { _ = reg.Close() }()
	reg.RegisterConfig(ServerConfig{Name: "dying", Command: cmd})

	// Publish the state a completed handshake would have produced. The real
	// handshake cannot be used: this command exits before speaking MCP, and a
	// server that dies *after* initializing is exactly the case under test.
	reg.mu.Lock()
	entry := reg.servers["dying"]
	client := entry.client
	entry.tools = []Tool{{Name: "doomed_tool"}}
	entry.ready = true
	reg.toolMap["doomed_tool"] = "dying"
	reg.mu.Unlock()

	if _, isErr := client.(*errClient); isErr {
		t.Fatal("expected a real stdio client; the command did not start")
	}

	// Sanity: the tool is advertised and resolvable while the entry is ready.
	if len(reg.AllTools()) != 1 {
		t.Fatalf("precondition: AllTools = %v, want 1 tool", reg.AllTools())
	}
	if _, ok := reg.FindToolServer("doomed_tool"); !ok {
		t.Fatal("precondition: FindToolServer should resolve before the child dies")
	}

	// The child has already exited or is about to. Its death must flip the bit.
	waitUntil(t, 10*time.Second, "ready to flip false after child death", func() bool {
		return !reg.IsReady("dying")
	})

	if tools := reg.AllTools(); len(tools) != 0 {
		t.Errorf("AllTools still advertises %v after the child died; the model would keep calling them", tools)
	}
	if _, ok := reg.FindToolServer("doomed_tool"); ok {
		t.Error("FindToolServer still resolves a dead client after the child died")
	}

	// The reason is retained and reachable, not just the fact.
	st := reg.Status()
	if len(st) != 1 {
		t.Fatalf("Status() returned %d entries, want 1", len(st))
	}
	if st[0].Ready {
		t.Error("Status() reports ready for a server whose child died")
	}
	if st[0].LastError == "" {
		t.Error("Status() lost the reason the server went down")
	}
}

// TestStdioChildDeathDoesNotClobberReplacement guards the re-registration race.
//
// A dead server's death notice is delivered asynchronously. If it were keyed on
// the server name alone it could land after a config reload had already spawned
// a healthy replacement under that name and mark the live one down for good,
// with nothing to bring it back.
func TestStdioChildDeathDoesNotClobberReplacement(t *testing.T) {
	cmd := shortLivedCmd(t)

	reg := NewRegistry()
	defer func() { _ = reg.Close() }()
	reg.RegisterConfig(ServerConfig{Name: "svc", Command: cmd})

	reg.mu.Lock()
	deadClient := reg.servers["svc"].client
	reg.mu.Unlock()

	// Re-register: RegisterConfig closes the old transport and installs a new
	// entry, exactly as a config reload does.
	reg.RegisterConfig(ServerConfig{Name: "svc", Command: cmd})

	reg.mu.Lock()
	live := reg.servers["svc"]
	liveClient := live.client
	live.ready = true
	reg.mu.Unlock()

	if liveClient == deadClient {
		t.Fatal("precondition: re-registration should have installed a new client")
	}

	// Deliver the stale notice for the transport that was replaced.
	reg.markUnready("svc", deadClient, errors.New("stale death notice"))

	if !reg.IsReady("svc") {
		t.Fatal("a stale death notice from a replaced transport marked the live server down")
	}

	// The live transport's own notice must still be honoured.
	reg.markUnready("svc", liveClient, errors.New("real death"))
	if reg.IsReady("svc") {
		t.Fatal("the live transport's death notice was ignored")
	}
}

// closedTransportClient models a stdio server whose process has gone: the
// handshake succeeded earlier, and calls now fail with ErrTransportClosed
// wrapped exactly as it reaches the executor in production — the library boxes
// it in *transport.Error (client.go), then stdioClient.CallTool adds its own
// %w. Detection depends on errors.Is seeing through both layers, so the test
// reproduces both rather than a single convenient wrap.
type closedTransportClient struct {
	calls int
}

func (c *closedTransportClient) Initialize(context.Context) (*ServerInfo, error) {
	return &ServerInfo{}, nil
}
func (c *closedTransportClient) ListTools(context.Context) ([]Tool, error) { return nil, nil }
func (c *closedTransportClient) CallTool(_ context.Context, name string, _ json.RawMessage) (*ToolCallResult, error) {
	c.calls++
	return nil, fmt.Errorf("mcp stdio tools/call %s: %w", name, transport.NewError(transport.ErrTransportClosed))
}
func (c *closedTransportClient) Close() error { return nil }

// TestTransportClosedSurvivesTheRealWrapChain pins the assumption detection
// rests on: ErrTransportClosed stays identifiable through the library's
// *transport.Error box and the gateway's own fmt.Errorf wrap. If a dependency
// bump broke either Unwrap, detection would silently stop working and this
// fails instead.
func TestTransportClosedSurvivesTheRealWrapChain(t *testing.T) {
	wrapped := fmt.Errorf("mcp stdio tools/call x: %w", transport.NewError(transport.ErrTransportClosed))
	if !errors.Is(wrapped, transport.ErrTransportClosed) {
		t.Fatalf("errors.Is lost ErrTransportClosed through the wrap chain: %v", wrapped)
	}
	// A different transport failure must not be mistaken for a dead process.
	other := fmt.Errorf("mcp stdio tools/call x: %w", transport.NewError(errors.New("read timeout")))
	if errors.Is(other, transport.ErrTransportClosed) {
		t.Error("an unrelated transport error was identified as a closed transport")
	}
}

// TestTransportClosedMarksServerUnready proves the reactive half of detection:
// one failed call is enough to withdraw a dead server, so the next call is not
// routed to it at all.
func TestTransportClosedMarksServerUnready(t *testing.T) {
	c := &closedTransportClient{}
	reg := registryWith(map[string]mcpClient{"s1": c})
	reg.mu.Lock()
	reg.servers["s1"].tools = []Tool{{Name: "t1"}}
	reg.toolMap["t1"] = "s1"
	reg.mu.Unlock()

	exec := NewExecutor(reg, 5, nil)
	msg := exec.executeToolCall(context.Background(), toolCallNamed("t1"))
	if msg.Content == "" {
		t.Fatal("expected an error payload for the LLM")
	}

	if reg.IsReady("s1") {
		t.Fatal("a transport-closed error did not withdraw the server")
	}
	if _, ok := reg.FindToolServer("t1"); ok {
		t.Error("FindToolServer still resolves the tool after transport closed")
	}
	if len(reg.AllTools()) != 0 {
		t.Error("AllTools still advertises tools from a server with a closed transport")
	}

	// The second call must not reach the dead client at all.
	before := c.calls
	exec.executeToolCall(context.Background(), toolCallNamed("t1"))
	if c.calls != before {
		t.Errorf("call %d was still routed to the dead client; want no further calls", c.calls)
	}
}

// TestErrClientLeavesNoExitWatcher covers the branch where the subprocess never
// started: there is no stderr pipe, so there is nothing to watch and
// RegisterConfig must not park a goroutine on a channel that never closes.
func TestErrClientLeavesNoExitWatcher(t *testing.T) {
	reg := NewRegistry()
	defer func() { _ = reg.Close() }()
	reg.RegisterConfig(ServerConfig{Name: "never-started", Command: "/nonexistent/mcp-server"})

	reg.mu.Lock()
	c := reg.servers["never-started"].client
	reg.mu.Unlock()

	if _, ok := c.(*errClient); !ok {
		t.Fatalf("expected errClient for a command that cannot start, got %T", c)
	}
	if w, ok := c.(interface{ Exited() <-chan struct{} }); ok && w.Exited() != nil {
		t.Error("errClient must not advertise an exit channel that never closes")
	}
}
