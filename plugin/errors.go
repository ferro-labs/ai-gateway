package plugin

import "fmt"

// RejectionError indicates a plugin intentionally rejected a request/response:
// the plugin ran, reached a decision, and that decision was "no". A blocked word,
// an exhausted rate limit, and a failed auth check are rejections.
//
// A plugin that could not reach a decision — because it errored or panicked —
// produces a FailureError instead. See that type for why the two are distinct.
type RejectionError struct {
	Plugin     string
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
// error or panicked. The request was not denied — it was never evaluated.
//
// It is a distinct type from RejectionError because the two carry opposite
// meanings: a rejection is a verdict on the client's request, a failure is the
// gateway's own component breaking. A failure is reported as a server error, so a
// rate-limit plugin whose backend is down does not answer 429 and invite every SDK
// to retry a limit that nothing will clear.
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
