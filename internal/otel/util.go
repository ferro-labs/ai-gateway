package otel

import "fmt"

// sprint formats v via fmt.Sprintf("%v", v). Centralised so callers
// don't pull fmt directly into hot files.
func sprint(v any) string { return fmt.Sprintf("%v", v) }
