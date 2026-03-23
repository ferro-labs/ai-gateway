// Package transport owns all HTTP transports used for upstream provider calls.
// Create a single Manager at startup — never per-request.
//
// Key design decisions:
//   - Per-provider client isolation prevents one slow provider from exhausting
//     connection pools shared by others.
//   - Separate streaming transport with no ResponseHeaderTimeout — LLM first
//     token latency can be 10-30s for large prompts.
//   - ForceAttemptHTTP2 for all providers — multiplexed connections reduce
//     head-of-line blocking.
//   - No global http.Client.Timeout — use context.WithTimeout per request
//     since streaming responses can legitimately take 60-120s.
package transport

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// Config holds transport configuration.
type Config struct {
	MaxIdleConnsPerHost   int
	MaxIdleConns          int
	MaxConnsPerHost       int
	IdleConnTimeout       time.Duration
	DialTimeout           time.Duration
	KeepAliveInterval     time.Duration
	TLSHandshakeTimeout   time.Duration
	ResponseHeaderTimeout time.Duration
	ForceHTTP2            bool
	DisableCompression    bool
	StreamingIdleTimeout  time.Duration
}

// DefaultConfig returns production-optimized defaults.
func DefaultConfig() Config {
	return Config{
		MaxIdleConnsPerHost:   100,
		MaxIdleConns:          1000,
		MaxConnsPerHost:       0,
		IdleConnTimeout:       90 * time.Second,
		DialTimeout:           10 * time.Second,
		KeepAliveInterval:     30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ForceHTTP2:            true,
		DisableCompression:    false,
		StreamingIdleTimeout:  5 * time.Minute,
	}
}

// Manager owns all HTTP transports.
// Create once at startup — never per-request.
type Manager struct {
	cfg           Config
	mu            sync.RWMutex
	providers     map[string]*http.Client
	defaultClient *http.Client
	streamClient  *http.Client
	metrics       *Metrics
}

// New creates a Manager with the given config.
func New(cfg Config) *Manager {
	m := &Manager{
		cfg:       cfg,
		providers: make(map[string]*http.Client),
		metrics:   newMetrics(),
	}
	m.defaultClient = m.buildClient(cfg, false)
	m.streamClient = m.buildClient(cfg, true)
	return m
}

// NewDefault creates a Manager with production defaults.
func NewDefault() *Manager {
	return New(DefaultConfig())
}

// ForProvider returns the HTTP client for a named provider.
// Falls back to default client if provider not registered.
// Thread-safe — uses RLock for fast concurrent reads.
func (m *Manager) ForProvider(provider string) *http.Client {
	m.mu.RLock()
	c, ok := m.providers[provider]
	m.mu.RUnlock()
	if ok {
		return c
	}
	return m.defaultClient
}

// ForStreaming returns the SSE-optimized client.
// No ResponseHeaderTimeout — first LLM token can take 10-30s.
func (m *Manager) ForStreaming(_ string) *http.Client {
	return m.streamClient
}

// RegisterProvider registers a custom-tuned client for a provider.
// Call at startup for known providers to pre-warm pools.
func (m *Manager) RegisterProvider(provider string, cfg Config) {
	client := m.buildClient(cfg, false)
	m.mu.Lock()
	m.providers[provider] = client
	m.mu.Unlock()
}

// Metrics returns the transport metrics for Prometheus registration.
func (m *Manager) Metrics() *Metrics {
	return m.metrics
}

// DefaultClient returns the shared default client.
func (m *Manager) DefaultClient() *http.Client {
	return m.defaultClient
}

// DefaultTransport returns the underlying *http.Transport of the default client.
func (m *Manager) DefaultTransport() *http.Transport {
	return m.defaultClient.Transport.(*http.Transport)
}

// CloseIdleConnections closes idle connections on all managed transports.
func (m *Manager) CloseIdleConnections() {
	m.defaultClient.CloseIdleConnections()
	m.streamClient.CloseIdleConnections()
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, c := range m.providers {
		c.CloseIdleConnections()
	}
}

func (m *Manager) buildClient(cfg Config, streaming bool) *http.Client {
	dialer := &net.Dialer{
		Timeout:   cfg.DialTimeout,
		KeepAlive: cfg.KeepAliveInterval,
	}

	idleTimeout := cfg.IdleConnTimeout
	if streaming {
		idleTimeout = cfg.StreamingIdleTimeout
	}

	t := &http.Transport{
		DialContext:           dialer.DialContext,
		MaxIdleConns:          cfg.MaxIdleConns,
		MaxIdleConnsPerHost:   cfg.MaxIdleConnsPerHost,
		MaxConnsPerHost:       cfg.MaxConnsPerHost,
		IdleConnTimeout:       idleTimeout,
		TLSHandshakeTimeout:   cfg.TLSHandshakeTimeout,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     cfg.ForceHTTP2,
		DisableKeepAlives:     false,
		DisableCompression:    cfg.DisableCompression,
	}

	// Never set ResponseHeaderTimeout for streaming —
	// waiting for first token from LLM can take 10-30s.
	if !streaming {
		t.ResponseHeaderTimeout = cfg.ResponseHeaderTimeout
	}

	return &http.Client{
		Transport: t,
		// No global Timeout — use context.WithTimeout per request.
		// LLM streaming responses can legitimately take 60-120s.
	}
}
