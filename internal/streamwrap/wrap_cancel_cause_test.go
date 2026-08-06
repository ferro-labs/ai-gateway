package streamwrap

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/streamio"
	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/pkg/metrics"
	"github.com/ferro-labs/ai-gateway/providers"
)

// TestMeter_AttributesCancellationByCause covers who gets the blame when a
// stream ends with its context cancelled.
//
// A stalled upstream and a caller hanging up both arrive as the same cancelled
// context, so the cause is the only thing separating them. Recorded without it,
// a provider that accepted the request and then sent nothing was counted as
// "client_canceled" and logged as "context canceled" — the label an operator
// uses to rule the gateway out, on a failure that is squarely the provider's.
func TestMeter_AttributesCancellationByCause(t *testing.T) {
	tests := []struct {
		name string
		// cause is handed to the cancel function; nil cancels without one, which
		// is what a caller disconnecting looks like.
		cause error
		// wantErrType is the gateway_provider_errors_total error_type label.
		wantErrType string
		// wantErrIs is what the recorded stream error must match — this is the
		// value the request log row and the span carry.
		wantErrIs error
	}{
		{
			name:        "caller hangs up",
			cause:       nil,
			wantErrType: "client_canceled",
			wantErrIs:   context.Canceled,
		},
		{
			name:        "gateway idle bound fires on a silent upstream",
			cause:       streamio.ErrIdleTimeout,
			wantErrType: "timeout",
			wantErrIs:   streamio.ErrIdleTimeout,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// A producer that never sends anything and closes only once the
			// consumer is gone: the hung-upstream shape.
			src := make(chan providers.StreamChunk)
			ctx, cancel := context.WithCancelCause(context.Background())
			go func() {
				<-ctx.Done()
				// Closing only after Meter has committed to its ctx.Done branch.
				// Closing the instant the context dies makes both select cases
				// ready at once, and the end-of-stream branch would then win at
				// random and report a clean finish.
				time.Sleep(30 * time.Millisecond)
				close(src)
			}()

			var recordedErr error
			errorFn := func(_ context.Context, err error, _ Measurements) {
				recordedErr = err
			}

			counter := metrics.ProviderErrors.WithLabelValues("openai", tt.wantErrType)
			before := counterValue(t, counter)

			out := Meter(ctx, src, time.Now(), MeterMeta{
				Provider:    "openai",
				Model:       "gpt-4o",
				MetricModel: "gpt-4o",
				Catalog:     models.Catalog{},
				ErrorFn:     errorFn,
			})

			cancel(tt.cause)

			done := make(chan struct{})
			go func() {
				for range out { //nolint:revive // empty-block: draining to completion
				}
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("Meter did not close out within 2s of cancel")
			}

			if delta := counterValue(t, counter) - before; delta < 1 {
				t.Errorf("provider_errors{error_type=%q} delta = %v, want >= 1", tt.wantErrType, delta)
			}
			if !errors.Is(recordedErr, tt.wantErrIs) {
				t.Errorf("recorded stream error = %v, want it to match %v", recordedErr, tt.wantErrIs)
			}
		})
	}
}

// TestMeter_IdleTimeoutIsNotClientCanceled states the misattribution directly:
// whatever else the idle bound is recorded as, it must not land in the counter
// an operator reads as "the caller went away".
func TestMeter_IdleTimeoutIsNotClientCanceled(t *testing.T) {
	src := make(chan providers.StreamChunk)
	ctx, cancel := context.WithCancelCause(context.Background())
	go func() {
		<-ctx.Done()
		close(src)
	}()

	canceledCounter := metrics.ProviderErrors.WithLabelValues("openai", "client_canceled")
	before := counterValue(t, canceledCounter)

	out := Meter(ctx, src, time.Now(), MeterMeta{
		Provider:    "openai",
		Model:       "gpt-4o",
		MetricModel: "gpt-4o",
		Catalog:     models.Catalog{},
	})
	cancel(streamio.ErrIdleTimeout)

	for range out { //nolint:revive // empty-block: draining to completion
	}

	if delta := counterValue(t, canceledCounter) - before; delta != 0 {
		t.Fatalf("client_canceled delta = %v, want 0 — a stalled upstream is not the caller's doing", delta)
	}
}

// errGatewayRequestDeadline stands in for aigateway.ErrRequestTimeout, the cause
// the gateway attaches to its own per-request deadline. streamwrap cannot import
// it, and it does not need to: what matters is that it is a cause the GATEWAY
// named, and any such cause is a bound it imposed on the upstream.
var errGatewayRequestDeadline = fmt.Errorf("gateway request timeout: %w", context.DeadlineExceeded)

// TestMeter_GatewayDeadlineIsNotClientCanceled covers the case the old
// stream-local switch got wrong. It asked only whether the cause was the idle
// bound, so EVERY other cause — including the gateway's own request deadline —
// read as the caller hanging up, and a provider that stalled past
// request_timeout was recorded under the label an operator uses to rule the
// gateway out. The shared classifier asks the question the other way round: a
// cause the stdlib did not produce is one this gateway attached.
func TestMeter_GatewayDeadlineIsNotClientCanceled(t *testing.T) {
	src := make(chan providers.StreamChunk)
	ctx, cancel := context.WithCancelCause(context.Background())
	go func() {
		<-ctx.Done()
		time.Sleep(30 * time.Millisecond)
		close(src)
	}()

	timeoutCounter := metrics.ProviderErrors.WithLabelValues("openai", metrics.ErrTypeTimeout)
	canceledCounter := metrics.ProviderErrors.WithLabelValues("openai", metrics.ErrTypeClientCanceled)
	timeoutBefore := counterValue(t, timeoutCounter)
	canceledBefore := counterValue(t, canceledCounter)

	out := Meter(ctx, src, time.Now(), MeterMeta{
		Provider:    "openai",
		Model:       "gpt-4o",
		MetricModel: "gpt-4o",
		Catalog:     models.Catalog{},
	})
	cancel(errGatewayRequestDeadline)

	for range out { //nolint:revive // empty-block: draining to completion
	}

	if delta := counterValue(t, timeoutCounter) - timeoutBefore; delta != 1 {
		t.Errorf("provider_errors{error_type=timeout} delta = %v, want 1", delta)
	}
	if delta := counterValue(t, canceledCounter) - canceledBefore; delta != 0 {
		t.Errorf("client_canceled delta = %v, want 0 — the gateway's own deadline is not the caller's doing", delta)
	}
}
