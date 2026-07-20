//go:build unix

package mcp

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// waitFor polls until cond holds or the deadline expires.
func waitFor(t *testing.T, d time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

// B3: the transport creates a stderr pipe and never reads it. Once the OS pipe
// buffer fills (64 KiB on Linux) the child blocks in write(2) and stops
// answering JSON-RPC entirely — a live process with a silent stdout and no
// recovery path. The drain is required for correctness, not just diagnostics.
func TestStdioClient_StderrIsDrained_NoDeadlock(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "finished")

	// Write ~150 KiB to stderr — comfortably past the pipe buffer — then record
	// that we got past it and idle so the process stays alive for Close().
	script := `i=0
while [ $i -lt 3000 ]; do
  echo "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" >&2
  i=$((i+1))
done
echo done > "` + marker + `"
exec cat`

	c := newStdioClient("stderr-flood", "sh", []string{"-c", script}, nil)
	t.Cleanup(func() { _ = c.Close() })

	if _, isErr := c.(*errClient); isErr {
		t.Fatal("failed to spawn test subprocess")
	}

	if !waitFor(t, 15*time.Second, func() bool {
		_, err := os.Stat(marker)
		return err == nil
	}) {
		t.Fatal("child never finished writing to stderr: the pipe filled and it is " +
			"blocked in write(2) — stderr is not being drained")
	}
}

// A single stderr line longer than the scanner's 64 KiB token limit stops Scan
// with ErrTooLong. Returning at that point would re-create the very deadlock the
// drain exists to prevent, so the drain must keep the pipe moving.
func TestStdioClient_StderrOverlongLineDoesNotDeadlock(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "finished")

	// One 200 KiB line with no newline, then a normal burst, then the marker.
	//
	// Generated with head+tr, not awk: building the string by concatenation in
	// awk is O(n^2) over 204800 iterations, which is tolerable under gawk and
	// pathological under mawk — it blew a 20s deadline on CI while finishing in
	// 1.5s locally.
	script := `head -c 204800 /dev/zero | tr '\0' 'a' >&2
i=0
while [ $i -lt 2000 ]; do
  echo "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" >&2
  i=$((i+1))
done
echo done > "` + marker + `"
exec cat`

	c := newStdioClient("stderr-overlong", "sh", []string{"-c", script}, nil)
	t.Cleanup(func() { _ = c.Close() })

	if _, isErr := c.(*errClient); isErr {
		t.Fatal("failed to spawn test subprocess")
	}

	if !waitFor(t, 20*time.Second, func() bool {
		_, err := os.Stat(marker)
		return err == nil
	}) {
		t.Fatal("child blocked after an over-long stderr line: the drain stopped " +
			"reading on ErrTooLong instead of discarding the remainder")
	}
}

// B4: npx and uvx exec the real MCP server as a separate process. os.Process.Kill
// signals "only the Process itself, not any other processes it may have started",
// so without a process group the real server survives every close and reload with
// the old pipes still open.
func TestStdioClient_CloseKillsGrandchild(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "grandchild.pid")

	// Stand in for `npx`: fork the real work into a separate process, record its
	// pid, and keep the leader alive so it is the one Close() signals.
	script := `sleep 300 &
echo $! > "` + pidFile + `"
exec cat`

	c := newStdioClient("grandchild-owner", "sh", []string{"-c", script}, nil)
	if _, isErr := c.(*errClient); isErr {
		t.Fatal("failed to spawn test subprocess")
	}

	if !waitFor(t, 10*time.Second, func() bool {
		b, err := os.ReadFile(pidFile) //nolint:gosec // test-owned path under t.TempDir()
		return err == nil && len(strings.TrimSpace(string(b))) > 0
	}) {
		t.Fatal("grandchild pid file never appeared")
	}

	raw, err := os.ReadFile(pidFile) //nolint:gosec // test-owned path under t.TempDir()
	if err != nil {
		t.Fatalf("read pid file: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("parse grandchild pid %q: %v", raw, err)
	}

	// Sanity: it is alive before we close.
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("grandchild %d not alive before Close: %v", pid, err)
	}

	if err := c.Close(); err != nil {
		t.Logf("Close returned %v (tolerated: the leader is not an MCP server)", err)
	}

	alive := func() bool { return syscall.Kill(pid, 0) == nil }
	if !waitFor(t, 10*time.Second, func() bool { return !alive() }) {
		_ = syscall.Kill(pid, syscall.SIGKILL) // do not leak the process out of the test
		t.Fatalf("grandchild %d survived Close(): the process group was not swept, "+
			"so an npx-style server outlives every close and reload", pid)
	}
}

// The guard exists because kill(-0, …) signals the caller's own process group:
// a failed Getpgid that fell through to that would make the gateway SIGKILL
// itself and every sibling in its group.
func TestSweepProcessGroup_RejectsUnownedPid(t *testing.T) {
	// A pid that cannot be ours. Getpgid must fail, and the sweep must report
	// that rather than broadcasting a signal.
	if err := sweepProcessGroup(-1); err == nil {
		t.Error("sweepProcessGroup(-1) = nil, want an error rather than a group-wide signal")
	}
}

// The subprocess must never inherit the gateway environment, and the failure
// mode is silent: os/exec treats a nil Cmd.Env as "inherit everything", so an
// empty base env plus no overrides would hand an MCP server every gateway
// credential rather than none.
func TestStdioClient_EmptyEnvDoesNotInheritGatewayEnvironment(t *testing.T) {
	// Strip every key the minimal base would otherwise pick up, so env ends up
	// empty — the exact condition that produced a nil slice.
	for _, k := range []string{"PATH", "HOME", "LANG", "TMPDIR"} {
		t.Setenv(k, "")
	}
	t.Setenv("FERRO_TEST_LEAKED_SECRET", "must-not-reach-subprocess")

	dir := t.TempDir()
	out := filepath.Join(dir, "env.txt")

	// envOverrides MUST stay nil here: a single override makes the slice
	// non-empty, which is exactly what stops it being nil, and the precondition
	// for the bug is that the base env AND the overrides are both empty.
	//
	// The shell expands the variable itself — no external binary — so an empty
	// PATH cannot make the probe silently no-op. An earlier version used bare
	// `env`, which PATH could not resolve; the redirect created the file anyway
	// and the assertion then passed against empty content, vacuously green
	// against the very bug it exists to catch. The "leaked=[" marker is the
	// anti-vacuous guard: it is present only if printf actually ran.
	c := newStdioClient("env-isolation", "/bin/sh",
		[]string{"-c", `printf 'leaked=[%s]\n' "$FERRO_TEST_LEAKED_SECRET" > ` + out + `; exec cat`},
		nil)
	t.Cleanup(func() { _ = c.Close() })

	if _, isErr := c.(*errClient); isErr {
		t.Fatal("failed to spawn test subprocess")
	}

	if !waitFor(t, 10*time.Second, func() bool {
		b, err := os.ReadFile(out) //nolint:gosec // test-owned path under t.TempDir()
		return err == nil && strings.Contains(string(b), "leaked=[")
	}) {
		t.Fatal("subprocess never reported its environment")
	}

	got, err := os.ReadFile(out) //nolint:gosec // test-owned path under t.TempDir()
	if err != nil {
		t.Fatalf("read env dump: %v", err)
	}
	dump := strings.TrimSpace(string(got))

	if !strings.Contains(dump, "leaked=[") {
		t.Fatalf("probe did not run (%q); the isolation assertion below would pass vacuously", dump)
	}
	if dump != "leaked=[]" {
		t.Errorf("subprocess inherited the gateway environment; isolation is inverted: %q", dump)
	}
}
