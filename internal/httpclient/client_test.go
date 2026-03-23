package httpclient

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestShared_NotNil(t *testing.T) {
	if Shared() == nil {
		t.Fatal("Shared() must not be nil")
	}
}

func TestForProvider_KnownProvider(t *testing.T) {
	// Known providers (registered via RegisterKnownProviders at init) must get
	// a dedicated pool — not the shared default client.
	oai := ForProvider("openai")
	if oai == nil {
		t.Fatal("ForProvider(\"openai\") must not be nil")
	}
	if oai == Shared() {
		t.Fatal("ForProvider(\"openai\") must return a dedicated client, not Shared()")
	}
}

func TestForProvider_UnknownProvider(t *testing.T) {
	// Unknown providers must fall back to the shared default client.
	unknown := ForProvider("some-unknown-provider")
	if unknown != Shared() {
		t.Fatal("ForProvider(unknown) must return Shared()")
	}
}

func TestSharedStreaming_NoTimeout(t *testing.T) {
	sc := SharedStreaming()
	if sc == nil {
		t.Fatal("SharedStreaming() must not be nil")
	}
	transport, ok := sc.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("streaming transport is %T, want *http.Transport", sc.Transport)
	}
	if transport.ResponseHeaderTimeout != 0 {
		t.Errorf("streaming ResponseHeaderTimeout = %v, want 0", transport.ResponseHeaderTimeout)
	}
}

func TestNew_WithTimeout(t *testing.T) {
	client := New(5 * time.Second)
	if client.Timeout != 5*time.Second {
		t.Errorf("timeout = %v, want 5s", client.Timeout)
	}
	if client.Transport != SharedTransport() {
		t.Error("New(timeout) must reuse SharedTransport()")
	}
}

func TestNew_ZeroTimeout(t *testing.T) {
	if New(0) != Shared() {
		t.Fatal("New(0) must return Shared()")
	}
}

func TestCloseIdleConnections_NoPanic(_ *testing.T) {
	// Must not panic even when called multiple times.
	CloseIdleConnections()
	CloseIdleConnections()
}

func TestManager_NotNil(t *testing.T) {
	if Manager() == nil {
		t.Fatal("Manager() must not be nil")
	}
}

func TestTracingTransport_RoundTripNilRequest(t *testing.T) {
	resp, err := newTracingTransport(SharedTransport()).RoundTrip(nil)
	if resp != nil && resp.Body != nil {
		defer func() {
			_ = resp.Body.Close()
		}()
	}
	if !errors.Is(err, errNilRequest) {
		t.Fatalf("RoundTrip(nil) error = %v, want %v", err, errNilRequest)
	}
	if resp != nil {
		t.Fatalf("RoundTrip(nil) response = %#v, want nil", resp)
	}
}
