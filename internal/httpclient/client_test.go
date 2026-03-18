package httpclient

import (
	"testing"
	"time"
)

func TestNew_NonPositiveTimeoutReturnsSharedClient(t *testing.T) {
	if got := New(0); got != Shared() {
		t.Fatalf("New(0) did not return the shared client")
	}
}

func TestNew_UsesSharedTransport(t *testing.T) {
	client := New(5 * time.Second)
	if client.Timeout != 5*time.Second {
		t.Fatalf("timeout = %v, want %v", client.Timeout, 5*time.Second)
	}
	if !UsesSharedTransport(client.Transport) {
		t.Fatalf("transport %T does not use shared transport", client.Transport)
	}
}

func TestSharedTransportPolicy(t *testing.T) {
	transport := SharedTransport()
	if transport.MaxConnsPerHost != defaultMaxConnsPerHost {
		t.Fatalf("MaxConnsPerHost = %d, want %d", transport.MaxConnsPerHost, defaultMaxConnsPerHost)
	}
	if transport.ResponseHeaderTimeout != defaultResponseHeaderTimout {
		t.Fatalf("ResponseHeaderTimeout = %v, want %v", transport.ResponseHeaderTimeout, defaultResponseHeaderTimout)
	}
	if transport.MaxResponseHeaderBytes != defaultMaxResponseHeaderB {
		t.Fatalf("MaxResponseHeaderBytes = %d, want %d", transport.MaxResponseHeaderBytes, defaultMaxResponseHeaderB)
	}
}

func TestSharedClientUsesSharedTransport(t *testing.T) {
	client := Shared()
	if !UsesSharedTransport(client.Transport) {
		t.Fatalf("shared client transport %T does not use shared transport", client.Transport)
	}
}
