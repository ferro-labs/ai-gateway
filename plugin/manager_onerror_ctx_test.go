package plugin

import (
	"context"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
)

// A mid-stream failure runs the on_error stage after the client has gone, so
// the request context is already cancelled by the time it runs. The stage that
// records what happened must still be able to write: on the streaming path that
// cancelled context turned the terminal request-log insert into "context
// canceled", and the failed request then appeared in no operator-facing surface
// at all — the exact harm this stage exists to prevent.
func TestRunOnError_DetachedFromRequestCancellation(t *testing.T) {
	m := NewManager(nil)

	var (
		ran         bool
		ctxErr      error
		hasDeadline bool
	)
	_ = m.Register(StageOnError, &mockPlugin{
		name: "recorder",
		typ:  TypeLogging,
		execFn: func(ctx context.Context, _ *Context) error {
			ran = true
			ctxErr = ctx.Err()
			_, hasDeadline = ctx.Deadline()
			return nil
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	m.RunOnError(ctx, NewContext(&providers.Request{}))

	if !ran {
		t.Fatal("on_error plugin did not run")
	}
	if ctxErr != nil {
		t.Fatalf("on_error ran on a cancelled context: %v", ctxErr)
	}
	// Detaching drops the request's deadline as well as its cancellation, so
	// the stage carries its own: a hung store must not hold the goroutine.
	if !hasDeadline {
		t.Error("on_error stage ran with no deadline of its own")
	}
}
