package aigateway

import (
	"context"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	// Register built-in plugins so benchmark helpers can load them via LoadPlugins.
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/maxtoken"
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/wordfilter"
)

// mockProviderName is the canonical provider name used by test mock providers.
const mockProviderName = "mock"

// mockProvider is a test double for providers.Provider.
type mockProvider struct {
	name       string
	models     []string
	resp       *providers.Response
	err        error
	completeFn func(context.Context, providers.Request) (*providers.Response, error)
}

func (m *mockProvider) Name() string                  { return m.name }
func (m *mockProvider) SupportedModels() []string     { return m.models }
func (m *mockProvider) Models() []providers.ModelInfo { return nil }
func (m *mockProvider) SupportsModel(model string) bool {
	for _, mm := range m.models {
		if mm == model {
			return true
		}
	}
	return false
}
func (m *mockProvider) Complete(ctx context.Context, req providers.Request) (*providers.Response, error) {
	if m.completeFn != nil {
		return m.completeFn(ctx, req)
	}
	return m.resp, m.err
}

type mockStreamProvider struct {
	mockProvider
	streamErr error
	streamCh  <-chan providers.StreamChunk
	streamFn  func(context.Context, providers.Request) (<-chan providers.StreamChunk, error)
}

func (m *mockStreamProvider) CompleteStream(ctx context.Context, req providers.Request) (<-chan providers.StreamChunk, error) {
	if m.streamErr != nil {
		return nil, m.streamErr
	}
	if m.streamFn != nil {
		return m.streamFn(ctx, req)
	}
	if m.streamCh != nil {
		return m.streamCh, nil
	}
	// Default: return an already-closed channel so streamwrap.Meter drains immediately.
	ch := make(chan providers.StreamChunk)
	close(ch)
	return ch, nil
}

func counterValue(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	m := &dto.Metric{}
	if err := c.Write(m); err != nil {
		t.Fatalf("failed to read counter value: %v", err)
	}
	return m.GetCounter().GetValue()
}

func requestMetricLabelExists(t *testing.T, provider, model, status string) bool {
	t.Helper()
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "gateway_requests_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			labels := map[string]string{}
			for _, label := range m.GetLabel() {
				labels[label.GetName()] = label.GetValue()
			}
			if labels["provider"] == provider && labels["model"] == model && labels["status"] == status {
				return true
			}
		}
	}
	return false
}

func ptrFloat64(v float64) *float64 { return &v }

func drainStream(t *testing.T, ch <-chan providers.StreamChunk) {
	t.Helper()
	for chunk := range ch {
		if chunk.Error != nil {
			t.Fatalf("stream chunk error: %v", chunk.Error)
		}
	}
}

func requireKeys(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got keys %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got keys %v, want %v", got, want)
		}
	}
}

// streamTargetOrder resolves the strategy exactly as RouteStream does and
// returns the streaming target order it selects, failing on any error.
func streamTargetOrder(t *testing.T, gw *Gateway, req providers.Request) []string {
	t.Helper()
	s, err := gw.getStrategy()
	if err != nil {
		t.Fatalf("getStrategy: %v", err)
	}
	keys, err := s.SelectTargets(req)
	if err != nil {
		t.Fatalf("SelectTargets: %v", err)
	}
	return keys
}

func assertSameTargetSet(t *testing.T, got []string, targets []Target) {
	t.Helper()
	want := make(map[string]bool, len(targets))
	for _, tgt := range targets {
		want[tgt.VirtualKey] = true
	}
	if len(got) != len(want) {
		t.Fatalf("SelectTargets = %v, want the %d configured targets", got, len(want))
	}
	for _, k := range got {
		if !want[k] {
			t.Fatalf("SelectTargets = %v, contains unexpected key %q", got, k)
		}
	}
}

func assertInTargets(t *testing.T, key string, targets []Target) {
	t.Helper()
	for _, tgt := range targets {
		if tgt.VirtualKey == key {
			return
		}
	}
	t.Fatalf("resolved provider %q not in configured targets %v", key, targets)
}

// TestRouteStream_And_Route_SameTargetOrder asserts Route (Strategy.Execute) and
// RouteStream (Strategy.SelectTargets) resolve consistently per strategy: for
// deterministic strategies both pick the same first target and SelectTargets
// exposes every configured target; for weighted-random strategies both pick
// within the configured target set from the one shared selection implementation.
