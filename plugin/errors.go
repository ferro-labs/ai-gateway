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
	return fmt.Sprintf("request rejected by %s (%s): %s", e.Plugin, e.Stage, e.Reason)
}
