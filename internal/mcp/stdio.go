package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/ferro-labs/ai-gateway/internal/version"
)

// stdioClient wraps a mark3labs MCP client for the stdio transport.
// It launches a subprocess and communicates via stdin/stdout pipes,
// converting between mark3labs protocol types and ferro-labs internal types.
type stdioClient struct {
	inner *mcpclient.Client
	// pgid of the child's process group, captured at spawn — the transport reaps
	// the leader before Close returns, so it cannot be looked up afterwards.
	// Zero when the process never started or the platform has no process groups.
	pgid int
	// exited is closed when the child's stderr reaches EOF, which happens once
	// the process and every descendant inheriting that descriptor have gone.
	// Nil when the transport exposed no stderr pipe to drain.
	exited chan struct{}
}

// Exited returns a channel closed when the subprocess is observed to have died.
//
// The signal is stderr EOF, which the gateway already watches because the drain
// goroutine must keep the pipe moving. EOF arrives only once every descendant
// that inherited the descriptor has closed it, so a launcher such as npx whose
// grandchild holds stderr open delays this signal for as long as that
// grandchild lives — the same process-tree case sweepProcessGroup exists for.
// Detection is therefore prompt for a direct child and best-effort for a tree;
// the reactive transport-closed check in the executor covers the delay.
func (c *stdioClient) Exited() <-chan struct{} { return c.exited }

// newStdioClient creates a stdio MCP client by launching command with args.
// name identifies the server in log records.
//
// The subprocess receives a minimal base environment (PATH, HOME, LANG, TMPDIR)
// plus any KEY=VALUE pairs from envOverrides. We use a custom CommandFunc so
// that gateway credentials (OPENAI_API_KEY, MASTER_KEY, etc.) are never
// inherited: the default mark3labs transport unconditionally prepends
// os.Environ() to whatever env slice is passed, so the only way to fully
// replace the environment is to supply a CommandFunc that sets cmd.Env
// directly without calling os.Environ().
// Returns an errClient instead of an error so that the Registry API (which has
// no error return) can defer the failure to InitializeAll, where it will be
// logged by the normal error path.
func newStdioClient(name, command string, args []string, envOverrides map[string]string) mcpClient {
	// Build a minimal base env — safe inherited variables only.
	//
	// Non-nil even when empty, and that is the whole isolation guarantee: os/exec
	// documents "If Env is nil, the new process uses the current process's
	// environment", so a nil slice here would silently hand the subprocess every
	// gateway credential. Reachable whenever PATH, HOME, LANG and TMPDIR are all
	// unset and the server declares no env of its own — an ordinary stripped
	// container (env -i, distroless).
	env := make([]string, 0, 4+len(envOverrides))
	for _, key := range []string{"PATH", "HOME", "LANG", "TMPDIR"} {
		if val := os.Getenv(key); val != "" {
			env = append(env, key+"="+val)
		}
	}
	for k, v := range envOverrides {
		env = append(env, k+"="+v)
	}

	// Capture env for the closure so the CommandFunc does not need to call
	// os.Environ(). The library passes c.env to cmdFunc, but we ignore that
	// parameter and use our already-built slice to keep the logic self-contained.
	isolatedEnv := env
	// The transport calls cmdFunc before cmd.Start, so the pid is not readable
	// here; keep the *exec.Cmd and read it once the constructor has returned.
	var spawned *exec.Cmd
	cmdFunc := transport.CommandFunc(func(ctx context.Context, command string, _ []string, args []string) (*exec.Cmd, error) {
		// INVARIANT: ctx here is context.Background() — mark3labs starts the
		// transport with it (client/stdio.go:40), so the child correctly
		// outlives individual requests and cmd.Cancel never fires. Do not
		// "fix" this to a request context: that would SIGKILL the MCP server
		// mid-flight whenever one request is cancelled.
		cmd := exec.CommandContext(ctx, command, args...) //nolint:gosec // command comes from gateway config, not user input
		cmd.Env = isolatedEnv
		configureProcGroup(cmd)
		// Bounds a child that ignores cancellation, and one that exits leaving
		// its pipes held open by an orphaned grandchild.
		cmd.WaitDelay = 10 * time.Second
		spawned = cmd
		return cmd, nil
	})

	c, err := mcpclient.NewStdioMCPClientWithOptions(command, nil, args, transport.WithCommandFunc(cmdFunc))
	if err != nil {
		return &errClient{err: fmt.Errorf("mcp stdio: start %q: %w", command, err)}
	}

	sc := &stdioClient{inner: c}
	if spawned != nil && spawned.Process != nil {
		// Setpgid made the child its own group leader, so pid == pgid.
		sc.pgid = spawned.Process.Pid
	}

	// Draining stderr is required for correctness, not just diagnostics: the
	// transport creates the pipe but never reads it, so once the OS pipe buffer
	// fills the child blocks in write(2) and stops answering JSON-RPC entirely.
	//
	// The drain's return doubles as the child-death signal: it can only happen
	// at EOF, and the gateway already owns the goroutine. Closing exited there
	// costs nothing and saves a second watcher.
	if r, ok := mcpclient.GetStderr(c); ok {
		sc.exited = make(chan struct{})
		go func() {
			defer close(sc.exited)
			drainStderr(r, name)
		}()
	}

	return sc
}

// drainStderr copies a child's stderr into the gateway log, one record per line.
//
// Debug level, never warn: the spec says a server MAY write anything to stderr,
// and that a client SHOULD NOT assume stderr output indicates an error.
func drainStderr(r io.Reader, server string) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		slog.Debug("mcp server stderr", "server", server, "line", scanner.Text())
	}
	// A single line past the scanner's 64 KiB token limit stops Scan with
	// ErrTooLong. Returning here would re-create the very deadlock this function
	// exists to prevent, so keep the pipe moving.
	if scanner.Err() != nil {
		_, _ = io.Copy(io.Discard, r)
	}
}

// Initialize performs the MCP initialization handshake over stdio.
// mark3labs automatically sends the notifications/initialized notification.
func (c *stdioClient) Initialize(ctx context.Context) (*ServerInfo, error) {
	req := mcpgo.InitializeRequest{}
	req.Params.ProtocolVersion = mcpgo.LATEST_PROTOCOL_VERSION
	req.Params.ClientInfo = mcpgo.Implementation{
		Name:    "ferro-ai-gateway",
		Version: version.Short(),
	}

	result, err := c.inner.Initialize(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("mcp stdio initialize: %w", err)
	}

	return &ServerInfo{
		Name:    result.ServerInfo.Name,
		Version: result.ServerInfo.Version,
	}, nil
}

// ListTools fetches the tool list over stdio and converts it to ferro-labs types
// via a JSON round-trip. The mark3labs Tool type marshals to the same JSON
// structure as the ferro-labs Tool type (name, description, inputSchema).
func (c *stdioClient) ListTools(ctx context.Context) ([]Tool, error) {
	result, err := c.inner.ListTools(ctx, mcpgo.ListToolsRequest{})
	if err != nil {
		return nil, fmt.Errorf("mcp stdio tools/list: %w", err)
	}

	toolsJSON, err := json.Marshal(result.Tools)
	if err != nil {
		return nil, fmt.Errorf("mcp stdio tools/list marshal: %w", err)
	}
	var tools []Tool
	if err := json.Unmarshal(toolsJSON, &tools); err != nil {
		return nil, fmt.Errorf("mcp stdio tools/list unmarshal: %w", err)
	}
	return tools, nil
}

// CallTool invokes a named tool over stdio. Arguments are unmarshaled from
// RawMessage into any for mark3labs, and the result is converted back to
// ferro-labs types via a JSON round-trip.
func (c *stdioClient) CallTool(ctx context.Context, name string, arguments json.RawMessage) (*ToolCallResult, error) {
	req := mcpgo.CallToolRequest{}
	req.Params.Name = name

	if len(arguments) > 0 {
		var args any
		if err := json.Unmarshal(arguments, &args); err != nil {
			return nil, fmt.Errorf("mcp stdio tools/call %s: unmarshal args: %w", name, err)
		}
		req.Params.Arguments = args
	}

	result, err := c.inner.CallTool(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("mcp stdio tools/call %s: %w", name, err)
	}

	// Convert via JSON: mark3labs content blocks share the same JSON structure
	// as ferro-labs ContentBlock (type, text, data, mimeType, resource fields).
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("mcp stdio tools/call %s: marshal result: %w", name, err)
	}
	var toolResult ToolCallResult
	if err := json.Unmarshal(resultJSON, &toolResult); err != nil {
		return nil, fmt.Errorf("mcp stdio tools/call %s: unmarshal result: %w", name, err)
	}
	return &toolResult, nil
}

// Close terminates the stdio subprocess and any descendant it left behind.
//
// The transport's own ladder (close stdin, 2s grace, SIGTERM, 3s, SIGKILL, then
// cmd.Wait) is already the shape the spec prescribes, so it runs first and
// unchanged. The sweep afterwards only reaches survivors — typically the real
// server that an npx or uvx leader exec'd into a separate process, which the
// ladder never signalled because it targets the leader pid alone.
func (c *stdioClient) Close() error {
	err := c.inner.Close()
	if c.pgid > 0 {
		// Best-effort: an already-empty group reports no error. Debug, not Warn —
		// the only reachable failure is a rare EPERM, which is not actionable and
		// must not page anyone.
		if sweepErr := sweepProcessGroup(c.pgid); sweepErr != nil {
			slog.Debug("mcp: process group sweep failed", "pgid", c.pgid, "error", sweepErr)
		}
	}
	return err
}

// ─── errClient ───────────────────────────────────────────────────────────────

// errClient is returned by newStdioClient when the subprocess cannot be started.
// Every method returns the construction-time error so that it surfaces in the
// normal InitializeAll error log rather than being silently swallowed.
type errClient struct {
	err error
}

func (e *errClient) Initialize(_ context.Context) (*ServerInfo, error) {
	return nil, e.err
}

func (e *errClient) ListTools(_ context.Context) ([]Tool, error) {
	return nil, e.err
}

func (e *errClient) CallTool(_ context.Context, _ string, _ json.RawMessage) (*ToolCallResult, error) {
	return nil, e.err
}

func (e *errClient) Close() error { return nil }
