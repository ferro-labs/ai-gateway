package transport

import "net/http"

// StreamTransport returns the underlying *http.Transport of the streaming
// client. The returned transport is the raw http.Transport, not the
// OTel-wrapping outer RoundTripper installed on the streaming client —
// callers that want OTel propagation should go through ForStreaming.
//
// Mirrors DefaultTransport: use this for transparent pass-through (e.g. the
// proxy) that needs the SSE tuning (no ResponseHeaderTimeout) without
// injecting traceparent headers or emitting an extra CLIENT span.
func (m *Manager) StreamTransport() *http.Transport {
	return m.streamTransport
}
