// Package fixture serves stable gateway response shapes for the CLI and TUI.
// The shapes mirror supported HTTP responses and deliberately omit
// fields whose contract is unknown.
package fixture

import "time"

// defaultKey is the credential Default() accepts. Master keys are `fgw_`-prefixed.
const defaultKey = "fgw_test"

// State picks which world the fake serves, so one handler covers the happy
// path, the degraded path, and the feature-absent path that drives the CLI's
// 404/501 degradation. Zero value = healthy and unauthenticated.
type State struct {
	RequireAuth bool   // admin routes 401 without a Bearer token
	AcceptKey   string // the credential RequireAuth accepts (default "fgw_test")
	// Degraded is a HEALTHY gateway in trouble: /health stays 200 "ok" with a
	// half-open circuit, and /readyz reports one target unroutable. Verified
	// against a live gateway — a circuit does not make /health non-200.
	Degraded bool
	// NoProviders is the separate axis: no credential is configured, so
	// /health is 503 "no_providers" with an EMPTY providers array. This is the
	// default state of a credential-free gateway, and it is the one thing that
	// makes /health non-200.
	NoProviders bool
	NoLogStore  bool          // /admin/logs and /admin/logs/stats return 501
	NoSessions  bool          // /admin/sessions returns 501
	NoMCP       bool          // omit mcp_servers from /readyz and /admin/health
	ChatFails   bool          // emit a mid-stream error frame instead of [DONE]
	ChatDelay   time.Duration // per-chunk delay; 0 in tests, ~60ms for a visible demo
}

// Default is the world most tests want: healthy, authenticated, every feature
// present.
func Default() State {
	return State{RequireAuth: true, AcceptKey: defaultKey}
}
