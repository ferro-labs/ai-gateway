package httpserver

import (
	"net"
	"net/http"
	"sync"

	"github.com/ferro-labs/ai-gateway/pkg/metrics"
)

type connTracker struct {
	mu     sync.Mutex
	states map[net.Conn]http.ConnState
}

func newConnTracker() *connTracker {
	return &connTracker{
		states: make(map[net.Conn]http.ConnState),
	}
}

// ConnState records state transitions and updates Prometheus gauges/counters.
func (t *connTracker) ConnState(conn net.Conn, state http.ConnState) {
	t.observe(conn, state)
}

func (t *connTracker) observe(conn net.Conn, state http.ConnState) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if prev, ok := t.states[conn]; ok {
		decrementConnectionGauge(prev)
	}

	switch state {
	case http.StateActive, http.StateIdle:
		incrementConnectionGauge(state)
		metrics.ServerConnectionTransitionsTotal.WithLabelValues(connStateLabel(state)).Inc()
		t.states[conn] = state
	case http.StateClosed, http.StateHijacked:
		metrics.ServerConnectionTransitionsTotal.WithLabelValues(connStateLabel(state)).Inc()
		delete(t.states, conn)
	default:
		metrics.ServerConnectionTransitionsTotal.WithLabelValues(connStateLabel(state)).Inc()
		t.states[conn] = state
	}
}

func incrementConnectionGauge(state http.ConnState) {
	label, ok := connectionGaugeLabel(state)
	if !ok {
		return
	}
	metrics.ServerConnectionsCurrent.WithLabelValues(label).Inc()
}

func decrementConnectionGauge(state http.ConnState) {
	label, ok := connectionGaugeLabel(state)
	if !ok {
		return
	}
	metrics.ServerConnectionsCurrent.WithLabelValues(label).Dec()
}

func connectionGaugeLabel(state http.ConnState) (string, bool) {
	switch state {
	case http.StateActive:
		return "active", true
	case http.StateIdle:
		return "idle", true
	default:
		return "", false
	}
}

func connStateLabel(state http.ConnState) string {
	switch state {
	case http.StateNew:
		return "new"
	case http.StateActive:
		return "active"
	case http.StateIdle:
		return "idle"
	case http.StateHijacked:
		return "hijacked"
	case http.StateClosed:
		return "closed"
	default:
		return "unknown"
	}
}
