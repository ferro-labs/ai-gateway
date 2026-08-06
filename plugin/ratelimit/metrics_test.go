package ratelimit

import (
	"context"
	"testing"

	"github.com/ferro-labs/ai-gateway/pkg/metrics"
	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers/core"
	dto "github.com/prometheus/client_model/go"
)

// pluginRejections reads the current gateway_rate_limit_rejections_total value
// for this plugin's key_type.
func pluginRejections(t *testing.T) float64 {
	t.Helper()
	counter, err := metrics.RateLimitRejections.GetMetricWithLabelValues(rejectionKeyType)
	if err != nil {
		t.Fatalf("get counter: %v", err)
	}
	var pb dto.Metric
	if err := counter.Write(&pb); err != nil {
		t.Fatalf("write counter: %v", err)
	}
	return pb.GetCounter().GetValue()
}

// TestRejectionsAreCounted is the regression test for the plugin limiter being
// invisible in gateway_rate_limit_rejections_total.
//
// The documented key_type set has always included "plugin", but this package
// did not import pkg/metrics at all, so a request the plugin shed produced a
// 429 to the caller and no sample anywhere. An operator watching the counter
// saw an idle gateway while it was rejecting everything.
func TestRejectionsAreCounted(t *testing.T) {
	tests := []struct {
		name   string
		config map[string]any
		pctx   *plugin.Context
	}{
		{
			name:   "global limiter",
			config: map[string]any{"requests_per_second": 1, "burst": 1},
			pctx:   &plugin.Context{Request: &core.Request{}},
		},
		{
			name:   "per-key limiter",
			config: map[string]any{"requests_per_second": 1000, "burst": 1000, "key_rpm": 1},
			pctx: &plugin.Context{
				Request:  &core.Request{},
				Metadata: map[string]any{"api_key": "k1"},
			},
		},
		{
			name:   "per-user limiter",
			config: map[string]any{"requests_per_second": 1000, "burst": 1000, "user_rpm": 1},
			pctx:   &plugin.Context{Request: &core.Request{User: "u1"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := newPlugin(t, tc.config)
			before := pluginRejections(t)

			// Spend the bucket, then keep going until one is rejected.
			var rejected bool
			for i := 0; i < 10 && !rejected; i++ {
				tc.pctx.Reject = false
				if err := p.Execute(context.Background(), tc.pctx); err != nil {
					t.Fatalf("Execute returned an error for a denial: %v", err)
				}
				rejected = tc.pctx.Reject
			}
			if !rejected {
				t.Fatal("precondition: the limiter never rejected, so the metric assertion proves nothing")
			}

			if got := pluginRejections(t); got <= before {
				t.Errorf("rejections counter = %v, was %v: a rejected request produced no sample", got, before)
			}
		})
	}
}

// TestAllowedRequestsAreNotCounted keeps the counter meaning what it says.
func TestAllowedRequestsAreNotCounted(t *testing.T) {
	p := newPlugin(t, map[string]any{"requests_per_second": 1000, "burst": 1000})
	pctx := &plugin.Context{Request: &core.Request{}}

	before := pluginRejections(t)
	if err := p.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if pctx.Reject {
		t.Fatal("precondition: the request was rejected by a limiter that should have allowed it")
	}
	if got := pluginRejections(t); got != before {
		t.Errorf("rejections counter moved from %v to %v on an allowed request", before, got)
	}
}
