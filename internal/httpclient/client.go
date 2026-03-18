// Package httpclient provides the shared process-wide HTTP client used by
// providers so connection pooling is reused consistently under load.
package httpclient

import (
	"net"
	"net/http"
	"time"
)

const (
	defaultDialTimeout            = 30 * time.Second
	defaultKeepAlive              = 30 * time.Second
	defaultMaxIdleConns           = 512
	defaultMaxConnsPerHost        = 256
	defaultMaxIdleConnsPerHost    = 128
	defaultIdleConnTimeout        = 90 * time.Second
	defaultTLSHandshakeTimeout    = 10 * time.Second
	defaultResponseHeaderTimeout  = 90 * time.Second
	defaultExpectContinueTimeout  = 1 * time.Second
	defaultMaxResponseHeaderBytes = 1 << 20 // 1 MiB
)

var sharedTransport = &http.Transport{
	Proxy: http.ProxyFromEnvironment,
	DialContext: (&net.Dialer{
		Timeout:   defaultDialTimeout,
		KeepAlive: defaultKeepAlive,
	}).DialContext,
	ForceAttemptHTTP2:      true,
	MaxIdleConns:           defaultMaxIdleConns,
	MaxConnsPerHost:        defaultMaxConnsPerHost,
	MaxIdleConnsPerHost:    defaultMaxIdleConnsPerHost,
	IdleConnTimeout:        defaultIdleConnTimeout,
	TLSHandshakeTimeout:    defaultTLSHandshakeTimeout,
	ResponseHeaderTimeout:  defaultResponseHeaderTimeout,
	ExpectContinueTimeout:  defaultExpectContinueTimeout,
	MaxResponseHeaderBytes: defaultMaxResponseHeaderBytes,
}

var sharedRoundTripper = newTracingTransport(sharedTransport)

var sharedClient = &http.Client{Transport: sharedRoundTripper}

// Shared returns the process-wide HTTP client used by providers so they reuse
// connection pools consistently under load.
func Shared() *http.Client {
	return sharedClient
}

// New returns a client that reuses the shared transport policy with an
// optional request timeout. A non-positive timeout reuses the shared client.
func New(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		return sharedClient
	}
	return &http.Client{
		Transport: sharedRoundTripper,
		Timeout:   timeout,
	}
}

// SharedTransport exposes the shared transport so other HTTP adapters can
// reuse the same pooling and timeout policy.
func SharedTransport() *http.Transport {
	return sharedTransport
}

// CloseIdleConnections closes any idle pooled connections held by the shared
// transport. Safe to call during shutdown.
func CloseIdleConnections() {
	sharedTransport.CloseIdleConnections()
}
