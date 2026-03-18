package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/metrics"
	dto "github.com/prometheus/client_model/go"
)

func TestServerConnTracker_TracksConnectionStates(t *testing.T) {
	tracker := newServerConnTracker()
	conn := testConn{
		local:  testAddr("127.0.0.1:8080"),
		remote: testAddr("203.0.113.5:1234"),
	}

	activeBefore := gaugeValue(t, metrics.ServerConnectionsCurrent.WithLabelValues("active"))
	idleBefore := gaugeValue(t, metrics.ServerConnectionsCurrent.WithLabelValues("idle"))
	closedBefore := counterValueMetric(t, metrics.ServerConnectionTransitionsTotal.WithLabelValues("closed"))

	tracker.observe(conn, http.StateNew)
	tracker.observe(conn, http.StateActive)
	if got := gaugeValue(t, metrics.ServerConnectionsCurrent.WithLabelValues("active")); got != activeBefore+1 {
		t.Fatalf("active connections = %v, want %v", got, activeBefore+1)
	}

	tracker.observe(conn, http.StateIdle)
	if got := gaugeValue(t, metrics.ServerConnectionsCurrent.WithLabelValues("active")); got != activeBefore {
		t.Fatalf("active connections after idle = %v, want %v", got, activeBefore)
	}
	if got := gaugeValue(t, metrics.ServerConnectionsCurrent.WithLabelValues("idle")); got != idleBefore+1 {
		t.Fatalf("idle connections = %v, want %v", got, idleBefore+1)
	}

	tracker.observe(conn, http.StateClosed)
	if got := gaugeValue(t, metrics.ServerConnectionsCurrent.WithLabelValues("idle")); got != idleBefore {
		t.Fatalf("idle connections after close = %v, want %v", got, idleBefore)
	}
	if got := counterValueMetric(t, metrics.ServerConnectionTransitionsTotal.WithLabelValues("closed")); got != closedBefore+1 {
		t.Fatalf("closed transitions = %v, want %v", got, closedBefore+1)
	}
}

func TestServerConnTracker_StoresConnMetadataInContext(t *testing.T) {
	tracker := newServerConnTracker()
	ctx := tracker.ConnContext(context.Background(), testConn{
		local:  testAddr("127.0.0.1:8080"),
		remote: testAddr("203.0.113.6:4567"),
	})

	meta, ok := connMetadataFromContext(ctx)
	if !ok {
		t.Fatal("expected connection metadata in context")
	}
	if meta.ID == 0 {
		t.Fatal("expected non-zero connection ID")
	}
	if meta.LocalAddr != "127.0.0.1:8080" {
		t.Fatalf("LocalAddr = %q, want %q", meta.LocalAddr, "127.0.0.1:8080")
	}
	if meta.RemoteAddr != "203.0.113.6:4567" {
		t.Fatalf("RemoteAddr = %q, want %q", meta.RemoteAddr, "203.0.113.6:4567")
	}
}

func gaugeValue(t *testing.T, g interface{ Write(*dto.Metric) error }) float64 {
	t.Helper()
	m := &dto.Metric{}
	if err := g.Write(m); err != nil {
		t.Fatalf("failed to read gauge: %v", err)
	}
	return m.GetGauge().GetValue()
}

func counterValueMetric(t *testing.T, c interface{ Write(*dto.Metric) error }) float64 {
	t.Helper()
	m := &dto.Metric{}
	if err := c.Write(m); err != nil {
		t.Fatalf("failed to read counter: %v", err)
	}
	return m.GetCounter().GetValue()
}

type testConn struct {
	local  net.Addr
	remote net.Addr
}

func (c testConn) Read(_ []byte) (int, error)         { return 0, io.EOF }
func (c testConn) Write(b []byte) (int, error)        { return len(b), nil }
func (c testConn) Close() error                       { return nil }
func (c testConn) LocalAddr() net.Addr                { return c.local }
func (c testConn) RemoteAddr() net.Addr               { return c.remote }
func (c testConn) SetDeadline(_ time.Time) error      { return nil }
func (c testConn) SetReadDeadline(_ time.Time) error  { return nil }
func (c testConn) SetWriteDeadline(_ time.Time) error { return nil }

type testAddr string

func (a testAddr) Network() string { return "tcp" }
func (a testAddr) String() string  { return string(a) }
