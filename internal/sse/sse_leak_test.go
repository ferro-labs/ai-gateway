package sse

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain installs a package-wide goroutine-leak check over the SSE writer's
// tests. Write drives per-write deadline timers and terminates on several paths
// (done sentinel, context cancel, idle timeout); none may leave a goroutine or
// timer running once the stream ends.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m, goleak.IgnoreCurrent())
}
