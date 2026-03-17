// Package httpclient provides the shared process-wide HTTP client used by
// providers so connection pooling is reused consistently under load.
package httpclient

import (
	"net"
	"net/http"
	"time"
)

const (
	defaultDialTimeout          = 30 * time.Second
	defaultKeepAlive            = 30 * time.Second
	defaultMaxIdleConns         = 512
	defaultMaxIdleConnsPerHost  = 128
	defaultIdleConnTimeout      = 90 * time.Second
	defaultTLSHandshakeTimeout  = 10 * time.Second
	defaultExpectContinueTimout = 1 * time.Second
)

var sharedClient = &http.Client{
	Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   defaultDialTimeout,
			KeepAlive: defaultKeepAlive,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          defaultMaxIdleConns,
		MaxIdleConnsPerHost:   defaultMaxIdleConnsPerHost,
		IdleConnTimeout:       defaultIdleConnTimeout,
		TLSHandshakeTimeout:   defaultTLSHandshakeTimeout,
		ExpectContinueTimeout: defaultExpectContinueTimout,
	},
}

// Shared returns the process-wide HTTP client used by providers so they reuse
// connection pools consistently under load.
func Shared() *http.Client {
	return sharedClient
}
