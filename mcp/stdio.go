package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/ferro-labs/ai-gateway/internal/version"
	"github.com/ferro-labs/ai-gateway/pkg/logger"
)

const (
	// maxStdioMessageBytes bounds a single JSON-RPC message read from a stdio
	// MCP server, matching the cap the HTTP transport already applies to a
	// response body.
	//
	// An MCP server is an untrusted-content boundary on both transports, and a
	// local one is arbitrary local code — at least as untrusted as a remote one.
	// The framing is one message per line, read with bufio.Reader.ReadString,
	// which accumulates without limit: a server that never emits a newline
	// drives gateway memory to whatever it cares to write, and the per-call
	// timeout bounds time, not memory.
	maxStdioMessageBytes = maxResponseBodyBytes // 10 MiB

	// stdioGraceTimeout is how long the child gets to exit on its own after its
	// stdin is closed — the shutdown the MCP spec prescribes — before teardown
	// escalates to SIGTERM.
	stdioGraceTimeout = 2 * time.Second
	// stdioKillTimeout bounds each rung past the grace period: the wait after
	// SIGTERM, the wait after SIGKILL, and the final reap.
	stdioKillTimeout = 3 * time.Second
)

// stdioClient wraps a mark3labs MCP client for the stdio transport.
// It launches a subprocess and communicates via stdin/stdout pipes,
// converting between mark3labs protocol types and ferro-labs internal types.
type stdioClient struct {
	inner *mcpclient.Client
	// cmd is the child the gateway spawned. The transport is built over its
	// pipes rather than being asked to spawn it, which is what allows a bound on
	// the stdout framing — and which makes the process lifecycle this type's to
	// run, in Close.
	cmd *exec.Cmd
	// closeOnce guards teardown: cmd.Wait may be called exactly once, and Close
	// is reachable twice (re-registration, then shutdown).
	closeOnce sync.Once
	closeErr  error
	// pgid of the child's process group, captured at spawn — the child is reaped
	// before Close returns, so it cannot be looked up afterwards.
	// Zero when the process never started or the platform has no process groups.
	pgid int
	// exited is closed when the child's stderr reaches EOF, which happens once
	// the process and every descendant inheriting that descriptor have gone.
	// Nil when the transport exposed no stderr pipe to drain.
	exited chan struct{}
}

// Exited returns a channel closed when the subprocess may have died.
//
// The signal is stderr EOF, which the gateway already watches because the drain
// goroutine must keep the pipe moving. It is a suspicion, not a verdict: the
// gateway holds only the read end, so a server that closes its own fd 2 while
// running produces the same EOF as one that exited. The registry confirms with
// a ping before withdrawing anything.
//
// EOF also arrives only once every descendant that inherited the descriptor has
// closed it, so a launcher such as npx whose grandchild holds stderr open
// delays the signal for as long as that grandchild lives — the same process-tree
// case sweepProcessGroup exists for. That death is caught reactively instead:
// writing to a pipe whose reader is gone fails with EPIPE, which the executor
// treats as conclusive alongside a closed transport.
func (c *stdioClient) Exited() <-chan struct{} { return c.exited }

// Ping issues an MCP ping, the mandatory server method used to confirm that a
// server suspected of having died is in fact gone. A live server answers; a
// transport whose reader already saw stdout EOF fails immediately without any
// I/O.
func (c *stdioClient) Ping(ctx context.Context) error { return c.inner.Ping(ctx) }

// newStdioClient creates a stdio MCP client by launching command with args.
// name identifies the server in log records.
//
// The subprocess receives a minimal base environment (PATH, HOME, LANG, TMPDIR)
// plus any KEY=VALUE pairs from envOverrides. The gateway builds the exec.Cmd
// itself and assigns cmd.Env directly, so gateway credentials (OPENAI_API_KEY,
// MASTER_KEY, etc.) are never inherited: the mark3labs transport prepends
// os.Environ() unconditionally to whatever env slice it is handed, so replacing
// the environment outright is only possible from outside it.
//
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

	// The gateway spawns the child itself rather than letting the transport do
	// it. transport.NewStdio wraps its own stdout in an unbounded reader and
	// exposes no hook to replace it, so owning the spawn is what lets
	// boundedLineReader sit in front of the framing. The cost is owning the
	// teardown ladder as well, which Close runs.
	//
	// INVARIANT: context.Background(), so cmd.Cancel never fires. The child must
	// outlive individual requests; binding it to a request context would SIGKILL
	// the MCP server mid-flight whenever one request is cancelled.
	cmd := exec.CommandContext(context.Background(), command, args...) //nolint:gosec // command comes from gateway config, not user input
	cmd.Env = env
	configureProcGroup(cmd)
	// Bounds a child that exits leaving its pipes held open by an orphaned
	// grandchild: Wait closes them and returns rather than blocking on a
	// descendant nobody is waiting for.
	cmd.WaitDelay = 10 * time.Second

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return &errClient{err: fmt.Errorf("mcp stdio: stdin pipe for %q: %w", command, err)}
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return &errClient{err: fmt.Errorf("mcp stdio: stdout pipe for %q: %w", command, err)}
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return &errClient{err: fmt.Errorf("mcp stdio: stderr pipe for %q: %w", command, err)}
	}

	if err := cmd.Start(); err != nil {
		return &errClient{err: fmt.Errorf("mcp stdio: start %q: %w", command, err)}
	}

	bounded := &boundedLineReader{r: stdout, server: name, limit: maxStdioMessageBytes}
	tr := transport.NewIO(bounded, stdin, stderr)
	// Start cannot spawn anything here: a NewIO transport carries no command, and
	// the transport returns early from spawning on that. All it does is launch
	// the reader goroutine. Checked anyway rather than discarded — a future
	// version that does more here must not fail silently.
	if err := tr.Start(context.Background()); err != nil {
		_ = cmd.Process.Kill()
		return &errClient{err: fmt.Errorf("mcp stdio: start transport for %q: %w", command, err)}
	}

	sc := &stdioClient{inner: mcpclient.NewClient(tr), cmd: cmd}
	if cmd.Process != nil {
		// Setpgid made the child its own group leader, so pid == pgid.
		sc.pgid = cmd.Process.Pid
	}

	// Draining stderr is required for correctness, not just diagnostics: nothing
	// else reads the pipe, so once the OS buffer fills the child blocks in
	// write(2) and stops answering JSON-RPC entirely.
	//
	// The drain's return doubles as the child-death signal: it can only happen
	// at EOF, and the gateway already owns the goroutine. Closing exited there
	// costs nothing and saves a second watcher.
	if r, ok := mcpclient.GetStderr(sc.inner); ok {
		sc.exited = make(chan struct{})
		go func() {
			defer close(sc.exited)
			drainStderr(r, name)
		}()
	}

	return sc
}

// boundedLineReader fails the read once a single newline-delimited message has
// run past limit bytes.
//
// It sits between the child's stdout and the transport's bufio.Reader, which
// frames JSON-RPC with ReadString('\n') and will otherwise accumulate a line of
// any length. The count resets at every newline, so the bound is per message,
// not per session.
//
// Exceeding it is terminal for the transport: the read loop treats any error as
// the server having gone, unblocking in-flight calls, and the registry withdraws
// the server after its ping fails. That is the right outcome — the alternative
// is resuming mid-message, with the framing permanently out of step.
type boundedLineReader struct {
	r      io.Reader
	server string
	limit  int
	n      int  // bytes of the message in progress; never allowed past limit
	failed bool // sticky: the bound was exceeded, and that is terminal
}

func (b *boundedLineReader) Read(p []byte) (int, error) {
	if b.failed {
		return 0, b.errTooLarge()
	}

	n, err := b.r.Read(p)
	if n == 0 {
		return n, err
	}

	// Walk the chunk one message at a time, and cut the returned slice at the
	// limit rather than reporting the error alongside the whole of it.
	//
	// That distinction is the guard, not a detail of it. bufio.Reader hands its
	// caller any complete line already sitting in its buffer *before* it reports
	// a stored read error, so returning a terminating newline together with the
	// error would let one oversized message through intact and fail only the
	// read after it — leaving the cap true of memory and false of the message.
	// Cutting before the byte that crosses the limit leaves the transport a
	// fragment with no newline in it, which it cannot mistake for a message.
	//
	// Walking every boundary rather than measuring the chunk's tail matters for
	// the same reason: the first newline ends the message carried in from
	// earlier reads — the only one whose running total can already be near the
	// limit — and a message that both ends and overruns inside one chunk is the
	// whole of a long message's final read.
	for off := 0; off < n; {
		room := b.limit - b.n
		i := bytes.IndexByte(p[off:n], '\n')
		if i < 0 {
			// The message runs on into the next read.
			if n-off > room {
				return off + room, b.errTooLarge()
			}
			b.n += n - off
			return n, err
		}
		if i > room {
			// It ends inside this chunk, but overruns before it does.
			return off + room, b.errTooLarge()
		}
		b.n = 0
		off += i + 1
	}
	return n, err
}

// errTooLarge marks the reader finished and names the server, which is the only
// record an operator gets of what tripped the bound.
//
// It replaces whatever the wrapped reader reported rather than deferring to it.
// A reader may legally deliver its final bytes with io.EOF attached, and on that
// read a plain end-of-stream would send whoever reads the log looking for a
// crashed server instead of an oversized message.
func (b *boundedLineReader) errTooLarge() error {
	b.failed = true
	return fmt.Errorf("mcp stdio: server %q sent a message over the %d byte limit", b.server, b.limit)
}

// drainStderr copies a child's stderr into the gateway log, one record per line.
//
// Debug level, never warn: the spec says a server MAY write anything to stderr,
// and that a client SHOULD NOT assume stderr output indicates an error.
func drainStderr(r io.Reader, server string) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		logger.Default().Debug("mcp server stderr", "server", server, "line", scanner.Text())
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
		// The HTTP transport WARNS on an unrecognised protocol version and
		// carries on, because a version this build does not list is usually
		// newer than the pinned library and the two calls the gateway makes —
		// tools/list and tools/call — have been stable across every revision.
		// stdio cannot do the same: the library refuses before returning a
		// result, so there is no initialised session to continue with, and
		// using the client anyway would mean driving an object the library
		// considers uninitialised.
		//
		// The divergence is therefore real and is the dependency's to resolve,
		// not something to paper over here. What this can do is say so, rather
		// than surfacing a bare protocol error an operator has to go and look
		// up.
		var unsupported mcpgo.UnsupportedProtocolVersionError
		if errors.As(err, &unsupported) {
			return nil, fmt.Errorf(
				"mcp stdio initialize: server negotiated protocol version %q, which this build does not recognise "+
					"(known: %v); the stdio transport cannot continue past this, though the HTTP transport only warns. "+
					"Upgrade the gateway, or run this server over HTTP: %w",
				unsupported.Version, mcpgo.ValidProtocolVersions, err)
		}
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
// inner.Close closes stdin — the shutdown the MCP spec prescribes — and unblocks
// any in-flight request. The ladder after it (grace period, SIGTERM, SIGKILL,
// reap) is the shape the transport used to run on the gateway's behalf, and runs
// here now that the gateway owns the spawn. The process-group sweep afterwards
// only reaches survivors — typically the real server that an npx or uvx leader
// exec'd into a separate process, which the ladder never signalled because it
// targets the leader pid alone.
func (c *stdioClient) Close() error {
	c.closeOnce.Do(func() {
		c.closeErr = c.inner.Close()
		if err := c.terminate(); err != nil && c.closeErr == nil {
			c.closeErr = err
		}
		if c.pgid > 0 {
			// Best-effort: an already-empty group reports no error. Debug, not Warn —
			// the only reachable failure is a rare EPERM, which is not actionable and
			// must not page anyone.
			if sweepErr := sweepProcessGroup(c.pgid); sweepErr != nil {
				logger.Default().Debug("mcp: process group sweep failed", "pgid", c.pgid, "error", sweepErr)
			}
		}
	})
	return c.closeErr
}

// terminate walks the shutdown ladder and reaps the child.
//
// cmd.Wait runs in a goroutine because every rung needs a bounded wait, and it
// is the only Wait there will ever be — Close's sync.Once guarantees that, and a
// second call would fail with "Wait was already called".
func (c *stdioClient) terminate() error {
	if c.cmd == nil {
		return nil
	}

	waited := make(chan error, 1)
	go func() { waited <- c.cmd.Wait() }()

	// signalled tracks whether the gateway actually delivered a signal, which is
	// what makes the status that follows its doing rather than the child's. It
	// is sticky: a child that survives SIGTERM and dies to it a moment later
	// refuses the SIGKILL with ErrProcessDone, and the status is still ours.
	signalled := false

	if done, err := waitForExit(waited, stdioGraceTimeout); done {
		return exitStatus(err, signalled)
	}
	if c.cmd.Process != nil {
		// A server that ignored its stdin closing — this rung is what that case
		// is for. A refusal is not a failure to report: the only one reachable is
		// a child that has already exited, whose status is then its own.
		signalled = terminateProcess(c.cmd.Process) == nil
	}
	if done, err := waitForExit(waited, stdioKillTimeout); done {
		return exitStatus(err, signalled)
	}
	if c.cmd.Process != nil {
		switch err := c.cmd.Process.Kill(); {
		case err == nil:
			signalled = true
		case !errors.Is(err, os.ErrProcessDone):
			return fmt.Errorf("mcp stdio: kill subprocess: %w", err)
		}
	}
	if done, err := waitForExit(waited, stdioKillTimeout); done {
		return exitStatus(err, signalled)
	}
	return errors.New("mcp stdio: subprocess did not exit after SIGKILL")
}

// exitStatus reports what Wait returned, dropping an exit status that the
// gateway's own signal produced.
//
// A child killed by teardown reports a non-zero status, and that status is
// teardown working. Returning it puts a "failed to close" warning in the log of
// every shutdown that needed a signal — the ordinary case for a server that does
// not watch its stdin — where noise hides the leak the warning exists to report.
//
// Only a signal the gateway delivered earns that. A child that exited on its own
// between the grace period and the signal refuses the signal, and the status it
// exited with is its own to surface.
//
// Anything that is not an exit status is a failure either way: a WaitDelay
// expiring on pipes an orphaned grandchild is holding open says the process tree
// outlived the ladder, which is exactly what the caller needs to hear.
func exitStatus(err error, signalled bool) error {
	if !signalled {
		return err
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return nil
	}
	return err
}

// waitForExit reports the child's exit status when it arrives within timeout.
// done is false when the rung expired with the child still running.
func waitForExit(waited <-chan error, timeout time.Duration) (done bool, err error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-waited:
		return true, err
	case <-timer.C:
		return false, nil
	}
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
