//go:build unix

package mcp

import (
	"fmt"
	"os/exec"
	"syscall"
)

// configureProcGroup puts the child in its own process group so the whole tree
// can be signalled at teardown.
//
// MCP servers are commonly launched via npx or uvx, which exec the real server
// as a separate process. os.Process.Kill signals "only the Process itself, not
// any other processes it may have started", so without a group the real server
// survives teardown holding the old pipes open, and they accumulate across every
// close and config reload.
//
// Setpgid makes the child the leader of a new group whose pgid equals its pid,
// which is why the pid captured at spawn is a valid group id later.
func configureProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// sweepProcessGroup force-kills every process remaining in the group led by
// pgid. Best-effort: an already-empty group is not a failure worth surfacing.
//
// pgid must be captured at spawn, not looked up at teardown. By the time this
// runs the transport has called cmd.Wait and reaped the leader, so Getpgid on
// that pid fails with ESRCH and the surviving descendants would never be
// signalled — which is precisely the bug this exists to fix.
//
// The pgid <= 1 guard is load-bearing, not padding. kill(-0, …) signals the
// *caller's* process group, so falling through on a zero value would make the
// gateway SIGKILL itself and every sibling in its group; -1 broadcasts to every
// process the user may signal.
func sweepProcessGroup(pgid int) error {
	if pgid <= 1 {
		return fmt.Errorf("mcp: refusing to signal process group %d", pgid)
	}
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil {
		if err == syscall.ESRCH {
			return nil // no survivors; the polite ladder was sufficient
		}
		return err
	}
	return nil
}
