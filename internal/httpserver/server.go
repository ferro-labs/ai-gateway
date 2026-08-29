// Package httpserver provides HTTP server construction helpers for the gateway.
package httpserver

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/httpclient"
)

// Default HTTP server timeouts and limits.
const (
	ServerReadTimeout       = 30 * time.Second
	ServerReadHeaderTimeout = 10 * time.Second
	ServerWriteTimeout      = 120 * time.Second
	ServerIdleTimeout       = 60 * time.Second
	ServerMaxHeaderBytes    = 1 << 20 // 1 MiB
)

// NamedResource pairs a human-readable name with a closeable value so that
// close errors can be reported with context.
type NamedResource struct {
	Name  string
	Value any
}

// NewServer constructs an *http.Server with the standard gateway timeouts and a
// connection tracker wired in for Prometheus metrics.
func NewServer(addr string, handler http.Handler) *http.Server {
	tracker := newConnTracker()
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ConnState:         tracker.ConnState,
		ReadTimeout:       ServerReadTimeout,
		ReadHeaderTimeout: ServerReadHeaderTimeout,
		WriteTimeout:      ServerWriteTimeout,
		IdleTimeout:       ServerIdleTimeout,
		MaxHeaderBytes:    ServerMaxHeaderBytes,
	}
}

// CloseResources closes every resource that implements io.Closer, joining any
// errors.  It always closes the shared HTTP client idle connections last.
func CloseResources(resources ...NamedResource) error {
	var err error
	for _, resource := range resources {
		closer, ok := resource.Value.(interface{ Close() error })
		if !ok || isNilPointer(resource.Value) {
			// A struct field that was never assigned arrives here as a typed nil:
			// the assertion succeeds, and Close dereferences it.
			continue
		}
		if closeErr := closer.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close %s: %w", resource.Name, closeErr))
		}
	}
	httpclient.CloseIdleConnections()
	return err
}

func isNilPointer(v any) bool {
	rv := reflect.ValueOf(v)
	return rv.Kind() == reflect.Pointer && rv.IsNil()
}
