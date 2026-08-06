package metrics

import (
	"fmt"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// counterValue reads the current value of a single Counter handle.
func counterValue(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	m := &dto.Metric{}
	if err := c.Write(m); err != nil {
		t.Fatalf("write counter: %v", err)
	}
	return m.GetCounter().GetValue()
}

// gatherMetric returns the first gathered series for the named metric family
// whose labels are a superset of want, or nil when no such series exists yet.
func gatherMetric(t *testing.T, name string, want map[string]string) *dto.Metric {
	t.Helper()
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			if labelsMatch(m, want) {
				return m
			}
		}
	}
	return nil
}

func labelsMatch(m *dto.Metric, want map[string]string) bool {
	got := make(map[string]string, len(m.GetLabel()))
	for _, l := range m.GetLabel() {
		got[l.GetName()] = l.GetValue()
	}
	for k, v := range want {
		if got[k] != v {
			return false
		}
	}
	return true
}

// gatheredCounter returns a counter series value via the default gatherer, or 0
// when the series has not been created. Callers assert deltas so the tests stay
// order-independent against the process-global registry.
func gatheredCounter(t *testing.T, name string, want map[string]string) float64 {
	t.Helper()
	if m := gatherMetric(t, name, want); m != nil {
		return m.GetCounter().GetValue()
	}
	return 0
}

// gatheredHistogramCount returns a histogram series observation count, or 0 when
// the series has not been created.
func gatheredHistogramCount(t *testing.T, name string, want map[string]string) uint64 {
	t.Helper()
	if m := gatherMetric(t, name, want); m != nil {
		return m.GetHistogram().GetSampleCount()
	}
	return 0
}

func TestUnknownModelLabel(t *testing.T) {
	if UnknownModelLabel != "unknown" {
		t.Fatalf("UnknownModelLabel = %q, want %q", UnknownModelLabel, "unknown")
	}
}

func TestForRequest_ReturnsUsableHandles(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		model    string
	}{
		{"known model", "openai", "gpt-4o"},
		{"unknown model bucket", "openai", UnknownModelLabel},
		{"empty model", "anthropic", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := ForRequest(tt.provider, tt.model)
			if h == nil {
				t.Fatal("ForRequest returned nil")
			}
			if h.Success == nil || h.Error == nil || h.Rejected == nil ||
				h.Duration == nil || h.TokensIn == nil || h.TokensOut == nil ||
				h.CostUSD == nil {
				t.Error("ForRequest returned a handle with a nil field")
			}
		})
	}
}

// TestForRequest_UnknownModelBucketing asserts a request whose model is unknown
// lands in the bounded "unknown" model series rather than an unbounded label.
func TestForRequest_UnknownModelBucketing(t *testing.T) {
	const provider = "bucket-provider"
	labels := map[string]string{"provider": provider, "model": UnknownModelLabel, "status": "rejected"}

	before := gatheredCounter(t, "gateway_requests_total", labels)
	ForRequest(provider, UnknownModelLabel).Rejected.Inc()
	after := gatheredCounter(t, "gateway_requests_total", labels)

	if delta := after - before; delta != 1 {
		t.Fatalf("rejected delta for unknown-model bucket = %v, want 1", delta)
	}
}

// TestForRequest_CountersObservable increments each cached counter handle and
// confirms the change is visible through the default gatherer.
func TestForRequest_CountersObservable(t *testing.T) {
	const provider, model = "count-provider", "count-model"
	h := ForRequest(provider, model)

	tests := []struct {
		name   string
		inc    func()
		metric string
		labels map[string]string
	}{
		{"success", h.Success.Inc, "gateway_requests_total", map[string]string{"provider": provider, "model": model, "status": "success"}},
		{"error", h.Error.Inc, "gateway_requests_total", map[string]string{"provider": provider, "model": model, "status": "error"}},
		{"rejected", h.Rejected.Inc, "gateway_requests_total", map[string]string{"provider": provider, "model": model, "status": "rejected"}},
		{"tokens_in", h.TokensIn.Inc, "gateway_tokens_input_total", map[string]string{"provider": provider, "model": model}},
		{"tokens_out", h.TokensOut.Inc, "gateway_tokens_output_total", map[string]string{"provider": provider, "model": model}},
		{"cost_usd", h.CostUSD.Inc, "gateway_request_cost_usd_total", map[string]string{"provider": provider, "model": model}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := gatheredCounter(t, tt.metric, tt.labels)
			tt.inc()
			after := gatheredCounter(t, tt.metric, tt.labels)
			if delta := after - before; delta != 1 {
				t.Fatalf("%s delta = %v, want 1", tt.metric, delta)
			}
		})
	}
}

func TestForRequest_DurationObservable(t *testing.T) {
	const provider, model = "dur-provider", "dur-model"
	labels := map[string]string{"provider": provider, "model": model}

	before := gatheredHistogramCount(t, "gateway_request_duration_seconds", labels)
	ForRequest(provider, model).Duration.Observe(0.123)
	after := gatheredHistogramCount(t, "gateway_request_duration_seconds", labels)

	if delta := after - before; delta != 1 {
		t.Fatalf("duration sample count delta = %d, want 1", delta)
	}
}

// TestForRequest_CachesHandles asserts equal labels reuse one handle while
// distinct labels get distinct handles.
func TestForRequest_CachesHandles(t *testing.T) {
	first := ForRequest("cache-provider", "cache-model")
	again := ForRequest("cache-provider", "cache-model")
	if first != again {
		t.Error("ForRequest did not reuse the cached handle for equal labels")
	}
	other := ForRequest("cache-provider", "other-model")
	if first == other {
		t.Error("ForRequest reused a handle across distinct labels")
	}
}

// TestForRequest_DistinctLabelsCached exercises many distinct labels and
// confirms each is cached consistently (the cache neither loses nor conflates
// entries across a spread of label sets).
func TestForRequest_DistinctLabelsCached(t *testing.T) {
	const provider = "spread-provider"
	seen := make(map[*RequestMetricHandles]string, 64)
	for i := 0; i < 64; i++ {
		model := fmt.Sprintf("model-%d", i)
		first := ForRequest(provider, model)
		if first != ForRequest(provider, model) {
			t.Fatalf("handle for %q not cached consistently", model)
		}
		if prev, dup := seen[first]; dup {
			t.Fatalf("handle for %q collided with %q", model, prev)
		}
		seen[first] = model
	}
}

// TestForProviderError_CachesAndIncrements covers the provider-error cache path
// and confirms increments are observable.
func TestForProviderError_CachesAndIncrements(t *testing.T) {
	first := ForProviderError("err-provider", "timeout")
	again := ForProviderError("err-provider", "timeout")
	if first != again {
		t.Error("ForProviderError did not reuse the cached counter for equal labels")
	}
	if other := ForProviderError("err-provider", "circuit_open"); first == other {
		t.Error("ForProviderError reused a counter across distinct error types")
	}

	before := counterValue(t, first)
	first.Inc()
	if delta := counterValue(t, first) - before; delta != 1 {
		t.Fatalf("provider error counter delta = %v, want 1", delta)
	}
}

// TestProviderInitFailures_Observable covers the startup-failure signal that
// callers bump directly on ProviderInitFailures.
func TestProviderInitFailures_Observable(t *testing.T) {
	const provider = "init-fail-provider"
	labels := map[string]string{"provider": provider}

	before := gatheredCounter(t, "gateway_provider_init_failures_total", labels)
	ProviderInitFailures.WithLabelValues(provider).Inc()
	after := gatheredCounter(t, "gateway_provider_init_failures_total", labels)

	if delta := after - before; delta != 1 {
		t.Fatalf("init failure delta = %v, want 1", delta)
	}
}
