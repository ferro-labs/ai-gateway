//go:build !unix

package mcp

import (
	"os"
	"os/exec"
)

// Windows process-tree teardown needs a Job Object (CREATE_NEW_PROCESS_GROUP
// plus AssignProcessToJobObject), which is a materially different mechanism from
// a POSIX process group. Until an MCP-on-Windows user asks for it, the shipped
// behaviour is the single-process teardown the transport already performs: an
// npx-style server may outlive Close here.
func configureProcGroup(*exec.Cmd) {}

// sweepProcessGroup is a no-op on platforms without POSIX process groups.
// The argument would be a group id captured at spawn; there is no group here.
func sweepProcessGroup(int) error { return nil }

// terminateProcess has no graceful rung here: os.Process.Signal(SIGTERM) is not
// implemented on Windows, so the polite step and the forceful one are the same
// call. The ladder in Close still runs; this rung simply ends the process
// sooner than it would elsewhere.
func terminateProcess(p *os.Process) error {
	return p.Kill()
}
