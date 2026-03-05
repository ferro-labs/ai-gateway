package plugin

import "fmt"

// RejectionError indicates a plugin intentionally rejected a request/response.
type RejectionError struct {
	Plugin string
	Stage  Stage
	Reason string
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
