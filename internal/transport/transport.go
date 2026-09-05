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
	"net/url"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/propagation"
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
		MaxIdleConnsPerHost: 100,
		MaxIdleConns:        1000,
		MaxConnsPerHost:     0,
		IdleConnTimeout:     90 * time.Second,
		DialTimeout:         10 * time.Second,
		KeepAliveInterval:   30 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		// Bounded by how long a model can take to answer, not by how long an
		// HTTP service can take to respond. A provider sends no header until it
		// has something to say, which for a non-streaming call is the whole
		// generation and for a streaming one is the first token — both of which
		// routinely pass thirty seconds on a reasoning model or a long prompt.
		// A provider whose observed behaviour differs sets its own value in
		// KnownProviderPresets, which overrides this.
		ResponseHeaderTimeout: 120 * time.Second,
		ForceHTTP2:            true,
		DisableCompression:    false,
		StreamingIdleTimeout:  5 * time.Minute,
	}
}

// Manager owns all HTTP transports.
// Create once at startup — never per-request.
type Manager struct {
	cfg                Config
	mu                 sync.RWMutex
	providers          map[string]*http.Client
	providerTransports map[string]*http.Transport // raw transports for inspection
	defaultClient      *http.Client
	streamClient       *http.Client
	defaultTransport   *http.Transport // raw transport for DefaultTransport()
	streamTransport    *http.Transport // raw streaming transport
}

// New creates a Manager with the given config.
func New(cfg Config) *Manager {
	m := &Manager{
		cfg:                cfg,
		providers:          make(map[string]*http.Client),
		providerTransports: make(map[string]*http.Transport),
	}
	m.defaultClient, m.defaultTransport = m.buildClient(cfg, false)
	m.streamClient, m.streamTransport = m.buildClient(cfg, true)
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

// RegisterProvider registers a custom-tuned client for a provider.
// Call at startup for known providers to pre-warm pools.
func (m *Manager) RegisterProvider(provider string, cfg Config) {
	client, raw := m.buildClient(cfg, false)
	m.mu.Lock()
	m.providers[provider] = client
	m.providerTransports[provider] = raw
	m.mu.Unlock()
}

// providerRawTransport returns the raw *http.Transport for a named
// provider, or the default raw transport when the provider was not
// registered.
func (m *Manager) providerRawTransport(provider string) *http.Transport {
	m.mu.RLock()
	t, ok := m.providerTransports[provider]
	m.mu.RUnlock()
	if ok {
		return t
	}
	return m.defaultTransport
}

// DefaultClient returns the shared default client.
func (m *Manager) DefaultClient() *http.Client {
	return m.defaultClient
}

// DefaultTransport returns the underlying *http.Transport of the
// default client. The returned transport is the raw http.Transport,
// not the OTel-wrapping outer RoundTripper installed on the client —
// callers that want OTel propagation should go through DefaultClient.
func (m *Manager) DefaultTransport() *http.Transport {
	return m.defaultTransport
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

// buildClient returns an *http.Client and the underlying raw
// *http.Transport. The client's Transport is UNCONDITIONALLY wrapped
// with otelhttp.NewTransport so every outbound provider call gets:
//   - traceparent injected into request headers
//   - a CLIENT span emitted by the OTel SDK
//
// The wrapper is applied regardless of whether OTel tracing is enabled.
// When no real TracerProvider is configured the global no-op tracer and
// no-op propagator are used, so no spans are exported; however there is
// a small per-RoundTrip overhead (~100 ns) even on the disabled path.
func (m *Manager) buildClient(cfg Config, streaming bool) (*http.Client, *http.Transport) {
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
		// Inbound baggage includes gateway-only user and session identity.
		// Keep outbound trace linkage without re-injecting that baggage.
		Transport: otelhttp.NewTransport(t, otelhttp.WithPropagators(propagation.TraceContext{})),
		// No global Timeout — use context.WithTimeout per request.
		// LLM streaming responses can legitimately take 60-120s.
		CheckRedirect: noRedirect,
	}, t
}

// noRedirect surfaces a 3xx to the caller instead of following it.
//
// These clients inject the operator's provider credential on every request. Go
// strips Authorization on a cross-domain hop but copies custom headers
// verbatim, so an upstream that redirects — whether compromised, or simply a
// misconfigured <PROVIDER>_BASE_URL — would have x-api-key, api-key and
// x-goog-api-key replayed to a host the operator never configured.
//
// Returning ErrUseLastResponse is not an error path: http.Client hands the
// caller the 3xx response with its body intact, so a legitimate redirect is
// visible and diagnosable rather than silently followed.
func noRedirect(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

// RedirectTarget describes where a surfaced 3xx pointed, for inclusion in the
// error a caller reports. It returns "" for any response that is not a
// redirect, so a caller can use it as the whole condition.
//
// Every client above refuses redirects identically, so without this the two
// cases an operator most needs to tell apart read the same: a same-host
// normalisation redirect (a trailing slash an MCP server adds, an ingress
// upgrading http to https) and a cross-host hop that would have replayed a
// credential. The first is the operator's own URL to fix; the second is the
// reason the policy exists.
//
// It names the scheme and host and nothing else. A Location URL can carry a
// credential in its userinfo or its query string, and url.URL keeps both out of
// Host — so the safe field is also the one that answers the question. A
// relative Location is resolved against the request so it too reports a host.
func RedirectTarget(resp *http.Response) string {
	if resp == nil || resp.StatusCode < 300 || resp.StatusCode > 399 {
		return ""
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		return "redirect with no Location header"
	}
	target, err := url.Parse(loc)
	if err != nil {
		return "redirect to an unparseable Location"
	}
	if resp.Request != nil && resp.Request.URL != nil {
		target = resp.Request.URL.ResolveReference(target)
		if target.Host == resp.Request.URL.Host {
			return "redirect to " + target.Scheme + "://" + target.Host + " (same host)"
		}
	}
	if target.Host == "" || target.Scheme == "" {
		return "redirect to a relative Location"
	}
	return "redirect to " + target.Scheme + "://" + target.Host
}
