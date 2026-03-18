package main

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/httpclient"
)

const (
	serverReadTimeout       = 30 * time.Second
	serverReadHeaderTimeout = 10 * time.Second
	serverWriteTimeout      = 120 * time.Second
	serverIdleTimeout       = 60 * time.Second
	serverMaxHeaderBytes    = 1 << 20 // 1 MiB
)

type namedResource struct {
	name  string
	value any
}

func newHTTPServer(addr string, handler http.Handler) *http.Server {
	tracker := newServerConnTracker()
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ConnContext:       tracker.ConnContext,
		ConnState:         tracker.ConnState,
		ReadTimeout:       serverReadTimeout,
		ReadHeaderTimeout: serverReadHeaderTimeout,
		WriteTimeout:      serverWriteTimeout,
		IdleTimeout:       serverIdleTimeout,
		MaxHeaderBytes:    serverMaxHeaderBytes,
	}
}

func closeResources(resources ...namedResource) error {
	var err error
	for _, resource := range resources {
		closer, ok := resource.value.(interface{ Close() error })
		if !ok {
			continue
		}
		if closeErr := closer.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close %s: %w", resource.name, closeErr))
		}
	}
	httpclient.CloseIdleConnections()
	return err
}
