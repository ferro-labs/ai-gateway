// Package httpclient provides the shared process-wide HTTP client used by
// providers so connection pooling is reused consistently under load.
//
// Internally delegates to the transport.Manager for production-optimized
// connection pool settings, HTTP/2 support, and separate streaming transport.
package httpclient

import (
	"net/http"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/transport"
)

// manager is the process-wide transport manager.
// Initialized once — all providers share this instance.
// Known providers are pre-registered with tuned pool settings.
var manager = initManager()

func initManager() *transport.Manager {
	m := transport.NewDefault()
	m.RegisterKnownProviders()
	return m
}

// ForProvider returns the per-provider HTTP client with tuned pool settings.
// Known providers (openai, anthropic, etc.) get isolated pools registered at
// init time via RegisterKnownProviders. Unknown providers fall back to the
// shared default client.
func ForProvider(name string) *http.Client {
	return manager.ForProvider(name)
}

// New returns a client that reuses the shared transport policy with an
// optional request timeout. A non-positive timeout reuses the shared client.
func New(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		return manager.DefaultClient()
	}
	return &http.Client{
		Transport: manager.DefaultTransport(),
		Timeout:   timeout,
		// Redirects are surfaced, not followed: this client carries MCP and
		// discovery credentials, and Go replays custom auth headers on a
		// cross-host hop. Same policy as the shared transport clients.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// SharedStreamingTransport exposes the raw SSE-tuned transport (no
// ResponseHeaderTimeout) WITHOUT the otelhttp wrapper, so a caller emits no
// extra OTel CLIENT span per call. The pass-through proxy uses it and injects
// trace context itself. Callers that want the CLIENT span as well should use
// SharedStreaming instead.
func SharedStreamingTransport() *http.Transport {
	return manager.StreamTransport()
}

// CloseIdleConnections closes any idle pooled connections held by the shared
// transport. Safe to call during shutdown.
func CloseIdleConnections() {
	manager.CloseIdleConnections()
}
