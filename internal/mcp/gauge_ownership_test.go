package mcp

import (
	"context"
	"testing"

	"github.com/ferro-labs/ai-gateway/internal/metrics"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// serverUpSeries reads the gateway_mcp_server_up sample for one server without
// creating it. Presence has to be distinguishable from a zero value here: the
// point of retiring a series is that it disappears rather than sits at 0.
func serverUpSeries(t *testing.T, name string) (float64, bool) {
	t.Helper()
	ch := make(chan prometheus.Metric, 128)
	metrics.MCPServerUp.Collect(ch)
	close(ch)

	for m := range ch {
		var pb dto.Metric
		if err := m.Write(&pb); err != nil {
			t.Fatalf("write metric: %v", err)
		}
		for _, label := range pb.GetLabel() {
			if label.GetName() == "server_name" && label.GetValue() == name {
				return pb.GetGauge().GetValue(), true
			}
		}
	}
	return 0, false
}

// TestRetiredRegistryDoesNotClobberLiveGauge is the regression test for a
// retiring registry writing the availability gauge of a server it no longer
// owns.
//
// gateway_mcp_server_up is labelled by server name alone, so every registry
// writes the same process-wide series. A configuration reload builds a
// replacement registry and retires the previous one, and retirement is deferred
// until in-flight requests release it — so the old registry's teardown lands
// after the replacement has already handshaken and reported the server up. Its
// write of 0 then stuck: InitializeAll skips an already-ready server, so nothing
// wrote 1 again for the life of that registry, and the gauge read down for a
// server that was serving.
func TestRetiredRegistryDoesNotClobberLiveGauge(t *testing.T) {
	const name = "gauge-handover"
	srv := newMockServer(t, []Tool{{Name: "gh_tool"}})
	defer srv.Close()

	cfg := ServerConfig{Name: name, URL: srv.URL, TimeoutSeconds: 5}

	retiring := NewRegistry()
	retiring.RegisterConfig(cfg)
	retiring.InitializeAll(context.Background(), func(n string, err error) {
		t.Fatalf("init %s: %v", n, err)
	})
	if v, _ := serverUpSeries(t, name); v != 1 {
		t.Fatalf("precondition: gauge = %v after the first registry initialized, want 1", v)
	}

	// The reload: a fresh registry takes over the same server name.
	live := NewRegistry()
	defer func() { _ = live.Close() }()
	live.RegisterConfig(cfg)
	live.InitializeAll(context.Background(), func(n string, err error) {
		t.Fatalf("init %s on the replacement: %v", n, err)
	})
	if v, _ := serverUpSeries(t, name); v != 1 {
		t.Fatalf("precondition: gauge = %v after the replacement initialized, want 1", v)
	}

	// The old registry's holders have drained and it tears down.
	if err := retiring.Close(); err != nil {
		t.Fatalf("close retiring registry: %v", err)
	}

	v, ok := serverUpSeries(t, name)
	if !ok {
		t.Fatal("the retiring registry deleted the live replacement's series")
	}
	if v != 1 {
		t.Errorf("gauge = %v after the retired registry tore down, want 1: the live server reports Ready but /metrics says it is down", v)
	}
	if !live.IsReady(name) {
		t.Fatal("precondition failed: the replacement is not ready, so the gauge assertion proves nothing")
	}
}

// TestRetiredRegistryDropsItsOwnSeries covers the other half: a server removed
// from the configuration kept a series pinned at 0 for the life of the process,
// so an alert on "server down" fired forever for a server nobody had asked for
// since the reload.
func TestRetiredRegistryDropsItsOwnSeries(t *testing.T) {
	const name = "gauge-removed"
	srv := newMockServer(t, []Tool{{Name: "gr_tool"}})
	defer srv.Close()

	reg := NewRegistry()
	reg.RegisterConfig(ServerConfig{Name: name, URL: srv.URL, TimeoutSeconds: 5})
	reg.InitializeAll(context.Background(), func(n string, err error) {
		t.Fatalf("init %s: %v", n, err)
	})
	if _, ok := serverUpSeries(t, name); !ok {
		t.Fatal("precondition: no series published for a registered server")
	}

	if err := reg.Close(); err != nil {
		t.Fatalf("close registry: %v", err)
	}

	if v, ok := serverUpSeries(t, name); ok {
		t.Errorf("series still present at %v after the only registry holding it retired; it never goes away", v)
	}
}
