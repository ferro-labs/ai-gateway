package httpclient

import (
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
	// The client's RoundTripper is the OTel-wrapping transport since
	// v1.1.0. The raw transport's ResponseHeaderTimeout is asserted in
	// transport.TestDefaultConfig — here we just check the client is
	// streaming-shaped (no client-level timeout).
	if sc.Timeout != 0 {
		t.Errorf("streaming client Timeout = %v, want 0", sc.Timeout)
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
