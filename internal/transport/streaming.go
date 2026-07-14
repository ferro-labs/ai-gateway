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

// IsStreamingRequest returns true if the request body contains
// "stream":true in any whitespace variation.
// Zero allocations — does not parse JSON, uses byte scanning only.
func IsStreamingRequest(body []byte) bool {
	// scan for "stream" then look for true after the colon
	for i := 0; i < len(body)-10; i++ {
		if body[i] != 's' {
			continue
		}
		if i+6 > len(body) || string(body[i:i+6]) != "stream" {
			continue
		}
		// found "stream" — scan forward for colon then true/false
		for j := i + 6; j < len(body) && j < i+30; j++ {
			switch body[j] {
			case ' ', '\t', '\n', '\r', '"', ':':
				continue
			case 't':
				return j+4 <= len(body) && string(body[j:j+4]) == "true"
			case 'f':
				return false
			}
		}
	}
	return false
}
