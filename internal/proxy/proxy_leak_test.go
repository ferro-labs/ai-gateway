package proxy

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain installs a package-wide goroutine-leak check over the pass-through
// proxy's tests. Streaming, cancellation, and connection-upgrade paths spawn
// copy goroutines and idle-timeout watchers that must all exit once the request
// completes or the upstream is cut.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m, goleak.IgnoreCurrent())
}
