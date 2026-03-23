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

// Shared returns the process-wide HTTP client used by providers so they reuse
// connection pools consistently under load.
func Shared() *http.Client {
	return manager.DefaultClient()
}

// ForProvider returns the per-provider HTTP client with tuned pool settings.
// Known providers (openai, anthropic, etc.) get isolated pools registered at
// init time via RegisterKnownProviders. Unknown providers fall back to the
// shared default client.
func ForProvider(name string) *http.Client {
	return manager.ForProvider(name)
}

// SharedStreaming returns the SSE-optimized client with no ResponseHeaderTimeout.
// Use for streaming requests where first LLM token can take 10-30s.
func SharedStreaming() *http.Client {
	return manager.ForStreaming("")
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
	}
}

// SharedTransport exposes the shared transport so other HTTP adapters can
// reuse the same pooling and timeout policy.
func SharedTransport() *http.Transport {
	return manager.DefaultTransport()
}

// Manager returns the underlying transport.Manager for direct access
// (e.g. per-provider client registration, metrics).
func Manager() *transport.Manager {
	return manager
}

// CloseIdleConnections closes any idle pooled connections held by the shared
// transport. Safe to call during shutdown.
func CloseIdleConnections() {
	manager.CloseIdleConnections()
}
