package metrics

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/pkg/circuitbreaker"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// errGatewayBound stands in for the gateway's own deadline sentinels
// (aigateway.ErrRequestTimeout, streamio.ErrIdleTimeout), which this package
// cannot import. The shape is what matters and it is deliberate: both WRAP
// context.DeadlineExceeded so packages that cannot name them can still answer
// "did this time out", which is exactly why ProviderErrorType must compare the
// cause by IDENTITY rather than with errors.Is.
var errGatewayBound = fmt.Errorf("gateway request timeout: %w", context.DeadlineExceeded)

func TestProviderErrorType(t *testing.T) {
	// canceled by the caller: no cause named, so the stdlib sentinel is returned.
	callerGone, cancelCaller := context.WithCancel(context.Background())
	cancelCaller()

	// a deadline the CALLER supplied: again a bare stdlib sentinel.
	callerDeadline, cancelDeadline := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancelDeadline()
	<-callerDeadline.Done()

	// a deadline the GATEWAY imposed on the upstream: it names its own cause.
	gatewayBound, cancelBound := context.WithCancelCause(context.Background())
	cancelBound(errGatewayBound)

	tests := []struct {
		name string
		ctx  context.Context
		err  error
		want string
	}{
		{
			name: "open circuit is the gateway declining, not a provider failing",
			ctx:  context.Background(),
			err:  fmt.Errorf("target openai: %w", circuitbreaker.ErrCircuitOpen),
			want: ErrTypeCircuitOpen,
		},
		{
			name: "concurrency shed never reached the provider",
			ctx:  context.Background(),
			err:  fmt.Errorf("target openai: %w", core.ErrProviderSaturated),
			want: ErrTypeBackpressure,
		},
		{
			name: "a shed still reads as backpressure once the caller has gone too",
			ctx:  callerGone,
			err:  core.ErrProviderSaturated,
			want: ErrTypeBackpressure,
		},
		{
			name: "caller hung up",
			ctx:  callerGone,
			err:  errors.New("read: connection reset"),
			want: ErrTypeClientCanceled,
		},
		{
			name: "caller's own deadline elapsed",
			ctx:  callerDeadline,
			err:  context.DeadlineExceeded,
			want: ErrTypeClientCanceled,
		},
		{
			name: "gateway's own bound elapsed: the provider was too slow",
			ctx:  gatewayBound,
			err:  errors.New("request aborted"),
			want: ErrTypeTimeout,
		},
		{
			name: "provider's own client timed out while our context stayed alive",
			ctx:  context.Background(),
			err:  fmt.Errorf("post https://api.example.com: %w", context.DeadlineExceeded),
			want: ErrTypeTimeout,
		},
		{
			name: "an ordinary upstream failure",
			ctx:  context.Background(),
			err:  errors.New("500 internal server error"),
			want: ErrTypeProvider,
		},
		{
			name: "a nil context contributes nothing and does not panic",
			ctx:  nil,
			err:  errors.New("500 internal server error"),
			want: ErrTypeProvider,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ProviderErrorType(tt.ctx, tt.err); got != tt.want {
				t.Errorf("ProviderErrorType = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestProviderErrorType_GatewayBoundIsNotTheCallersDoing states the identity
// comparison directly. The gateway's deadline sentinels wrap
// context.DeadlineExceeded, so a classifier reaching for errors.Is would file
// every stalled upstream under the label an operator reads as "nothing here is
// at fault" — and the circuit breaker, which trips on exactly this case, would
// then contradict the metric.
func TestProviderErrorType_GatewayBoundIsNotTheCallersDoing(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(errGatewayBound)

	if !errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
		t.Fatal("premise broken: the gateway sentinel must wrap context.DeadlineExceeded")
	}
	if got := ProviderErrorType(ctx, errors.New("upstream sent nothing")); got != ErrTypeTimeout {
		t.Fatalf("ProviderErrorType = %q, want %q", got, ErrTypeTimeout)
	}
}
