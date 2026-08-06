package transport

import (
	"testing"
	"time"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.MaxIdleConnsPerHost != 100 {
		t.Errorf("MaxIdleConnsPerHost = %d, want 100", cfg.MaxIdleConnsPerHost)
	}
	if cfg.MaxIdleConns != 1000 {
		t.Errorf("MaxIdleConns = %d, want 1000", cfg.MaxIdleConns)
	}
	if !cfg.ForceHTTP2 {
		t.Error("ForceHTTP2 = false, want true")
	}

	// Streaming client must have no ResponseHeaderTimeout. Read the raw
	// transport directly — the client's RoundTripper is the OTel
	// wrapper since v1.1.0.
	m := NewDefault()
	if m.streamTransport.ResponseHeaderTimeout != 0 {
		t.Errorf("streaming ResponseHeaderTimeout = %v, want 0", m.streamTransport.ResponseHeaderTimeout)
	}

	// Default client must have ResponseHeaderTimeout set.
	if m.defaultTransport.ResponseHeaderTimeout != cfg.ResponseHeaderTimeout {
		t.Errorf("default ResponseHeaderTimeout = %v, want %v", m.defaultTransport.ResponseHeaderTimeout, cfg.ResponseHeaderTimeout)
	}
}

func TestForProvider_Isolation(t *testing.T) {
	m := NewDefault()

	openaiCfg := DefaultConfig()
	openaiCfg.MaxIdleConnsPerHost = 50
	m.RegisterProvider("openai", openaiCfg)

	anthropicCfg := DefaultConfig()
	anthropicCfg.MaxIdleConnsPerHost = 30
	m.RegisterProvider("anthropic", anthropicCfg)

	oaiClient := m.ForProvider("openai")
	antClient := m.ForProvider("anthropic")
	defClient := m.ForProvider("unknown-provider")

	if oaiClient == antClient {
		t.Error("openai and anthropic clients must be different instances")
	}
	if defClient != m.defaultClient {
		t.Error("unregistered provider must return defaultClient")
	}
	if oaiClient == m.defaultClient {
		t.Error("registered provider must NOT return defaultClient")
	}

	// Verify transport configs are isolated. The Client.Transport is the
	// OTel wrapper since v1.1.0 — read raw transports from the manager.
	oaiTransport := m.providerRawTransport("openai")
	antTransport := m.providerRawTransport("anthropic")
	if oaiTransport.MaxIdleConnsPerHost != 50 {
		t.Errorf("openai MaxIdleConnsPerHost = %d, want 50", oaiTransport.MaxIdleConnsPerHost)
	}
	if antTransport.MaxIdleConnsPerHost != 30 {
		t.Errorf("anthropic MaxIdleConnsPerHost = %d, want 30", antTransport.MaxIdleConnsPerHost)
	}
	_ = oaiClient
	_ = antClient
}

func TestBufferPool(t *testing.T) {
	buf := BufferPool.Get()
	if buf.Len() != 0 {
		t.Errorf("fresh buffer Len = %d, want 0", buf.Len())
	}

	initialCap := buf.Cap()
	buf.WriteString("hello world")
	if buf.Len() != 11 {
		t.Errorf("after write Len = %d, want 11", buf.Len())
	}

	BufferPool.Put(buf)

	// Get again — should be reset but cap preserved.
	buf2 := BufferPool.Get()
	if buf2.Len() != 0 {
		t.Errorf("recycled buffer Len = %d, want 0", buf2.Len())
	}
	if buf2.Cap() < initialCap {
		t.Errorf("recycled buffer Cap = %d, want >= %d", buf2.Cap(), initialCap)
	}
	BufferPool.Put(buf2)
}

func TestBufferPool_OversizedDiscard(t *testing.T) {
	buf := BufferPool.Get()
	// Grow past 1MB threshold.
	buf.Grow(2 * 1024 * 1024)
	bigCap := buf.Cap()
	BufferPool.Put(buf) // should be discarded

	// Next Get should return a fresh buffer, not the oversized one.
	buf2 := BufferPool.Get()
	if buf2.Cap() >= bigCap {
		t.Errorf("oversized buffer should have been discarded, got cap=%d", buf2.Cap())
	}
	BufferPool.Put(buf2)
}

func TestCloseIdleConnections(_ *testing.T) {
	m := NewDefault()
	m.RegisterProvider("test", DefaultConfig())
	// Should not panic.
	m.CloseIdleConnections()
}

func TestDefaultClient(t *testing.T) {
	m := NewDefault()
	if m.DefaultClient() == nil {
		t.Error("DefaultClient() must not be nil")
	}
	if m.DefaultTransport() == nil {
		t.Error("DefaultTransport() must not be nil")
	}
}

// The default header timeout bounds how long a provider may take to say
// anything, which for an LLM is a whole generation or a first token rather than
// the round trip of an ordinary HTTP service. A provider with no preset gets
// this value, so it has to cover a reasoning model on a long prompt.
func TestDefaultConfig_ResponseHeaderTimeoutCoversModelLatency(t *testing.T) {
	const slowestPlausibleFirstToken = 90 * time.Second

	got := DefaultConfig().ResponseHeaderTimeout
	if got < slowestPlausibleFirstToken {
		t.Errorf("default ResponseHeaderTimeout = %v, want at least %v: a provider without a preset "+
			"aborts a slow first token at this bound", got, slowestPlausibleFirstToken)
	}
	if got == 0 {
		t.Error("default ResponseHeaderTimeout must stay finite: it is the only bound on a provider " +
			"that accepts a connection and never answers when request_timeout is unset")
	}
}
