package httpclient

import "net/http"

// UsesSharedTransport reports whether rt is the package's shared transport.
func UsesSharedTransport(rt http.RoundTripper) bool {
	return rt == manager.DefaultTransport()
}
