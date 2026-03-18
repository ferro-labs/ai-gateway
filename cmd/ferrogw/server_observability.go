package main

import (
	"context"
	"net"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/ferro-labs/ai-gateway/internal/metrics"
)

type connContextKey struct{}

type connMetadata struct {
	ID         uint64
	LocalAddr  string
	RemoteAddr string
}

type serverConnTracker struct {
	nextID atomic.Uint64

	mu     sync.Mutex
	states map[net.Conn]http.ConnState
}

func newServerConnTracker() *serverConnTracker {
	return &serverConnTracker{
		states: make(map[net.Conn]http.ConnState),
	}
}

func (t *serverConnTracker) ConnContext(ctx context.Context, conn net.Conn) context.Context {
	meta := connMetadata{
		ID:         t.nextID.Add(1),
		LocalAddr:  conn.LocalAddr().String(),
		RemoteAddr: conn.RemoteAddr().String(),
	}
	return context.WithValue(ctx, connContextKey{}, meta)
}

func (t *serverConnTracker) ConnState(conn net.Conn, state http.ConnState) {
	t.observe(conn, state)
}

func (t *serverConnTracker) observe(conn net.Conn, state http.ConnState) {
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

func connMetadataFromContext(ctx context.Context) (connMetadata, bool) {
	meta, ok := ctx.Value(connContextKey{}).(connMetadata)
	return meta, ok
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
