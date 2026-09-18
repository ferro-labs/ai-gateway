package plugin

import "fmt"

// RejectionError indicates a plugin intentionally rejected a request/response:
// the plugin ran, reached a decision, and that decision was "no". A blocked word,
// an exhausted rate limit, and a failed auth check are rejections. The mapped
// HTTP status depends on stage and on what was denied: before the request, 400
// by default, 429 for a rate limit — which a caller can resolve by waiting —
// and 402 for an exhausted budget, which it cannot; 502 after it. See
// internal/apierror.RouteErrorDetails for the exact mapping.
//
// A plugin that could not reach a decision — because it errored or panicked —
// produces a FailureError instead. See that type for why the two are distinct.
type RejectionError struct {
	Plugin string
	// Instance is the operator-supplied id of the configured instance that
	// produced this rejection (PluginConfig.ID), or "" when none was set. Since
	// several instances of one plugin can be configured, Plugin alone cannot say
	// which rule fired; Instance can. It is an opaque string echoed from config,
	// never interpreted, and it is deliberately absent from Error() — it is a
	// field for programmatic attribution, not part of the client-facing message.
	Instance   string
	PluginType PluginType
	Stage      Stage
	Reason     string
}

// Error implements the error interface.
func (e *RejectionError) Error() string {
	switch e.Stage {
	case StageBeforeRequest:
		return fmt.Sprintf("request rejected by %s (%s): %s", e.Plugin, e.Stage, e.Reason)
	case StageAfterRequest:
		return fmt.Sprintf("response rejected by %s (%s): %s", e.Plugin, e.Stage, e.Reason)
	default:
		return fmt.Sprintf("rejected by %s (%s): %s", e.Plugin, e.Stage, e.Reason)
	}
}

// FailureError indicates a fail-closed plugin could not complete: it returned an
// error or panicked. The request was not denied — it was never evaluated, so the
// gateway reports it as a 500 server error rather than a rejection.
type FailureError struct {
	Plugin     string
	PluginType PluginType
	Stage      Stage
	Err        error
}

// Error implements the error interface.
func (e *FailureError) Error() string {
	return fmt.Sprintf("plugin %s (%s) failed at %s: %v", e.Plugin, e.PluginType, e.Stage, e.Err)
}

// Unwrap exposes the plugin's own error so callers can inspect the cause.
func (e *FailureError) Unwrap() error { return e.Err }
