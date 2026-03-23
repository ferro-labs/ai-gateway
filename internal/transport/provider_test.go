package transport

import (
	"testing"
	"time"
)

func TestKnownProviderPresets(t *testing.T) {
	presets := KnownProviderPresets()

	// Must have presets for high-traffic providers.
	required := []string{"openai", "anthropic", "gemini", "bedrock", "groq", "ollama"}
	for _, name := range required {
		if _, ok := presets[name]; !ok {
			t.Errorf("missing preset for %q", name)
		}
	}

	// OpenAI must have the largest pool.
	oai := presets["openai"]
	if oai.MaxIdleConnsPerHost < 200 {
		t.Errorf("openai MaxIdleConnsPerHost = %d, want >= 200", oai.MaxIdleConnsPerHost)
	}

	// Bedrock must have higher timeouts for cold starts.
	bed := presets["bedrock"]
	if bed.ResponseHeaderTimeout < 60*time.Second {
		t.Errorf("bedrock ResponseHeaderTimeout = %v, want >= 60s", bed.ResponseHeaderTimeout)
	}
	if bed.DialTimeout < 10*time.Second {
		t.Errorf("bedrock DialTimeout = %v, want >= 10s", bed.DialTimeout)
	}

	// Ollama must have small pool (local, low traffic).
	oll := presets["ollama"]
	if oll.MaxIdleConnsPerHost > 30 {
		t.Errorf("ollama MaxIdleConnsPerHost = %d, want <= 30", oll.MaxIdleConnsPerHost)
	}
}

func TestApplyPreset(t *testing.T) {
	base := DefaultConfig()

	// Apply a partial preset — only overrides non-zero fields.
	preset := ProviderPreset{
		MaxIdleConnsPerHost: 42,
		// DialTimeout left zero — should keep base value.
	}

	result := applyPreset(base, preset)
	if result.MaxIdleConnsPerHost != 42 {
		t.Errorf("MaxIdleConnsPerHost = %d, want 42", result.MaxIdleConnsPerHost)
	}
	if result.DialTimeout != base.DialTimeout {
		t.Errorf("DialTimeout = %v, want %v (base default)", result.DialTimeout, base.DialTimeout)
	}
	if result.ForceHTTP2 != base.ForceHTTP2 {
		t.Error("ForceHTTP2 should be preserved from base")
	}
}

func TestRegisterKnownProviders(t *testing.T) {
	m := NewDefault()
	m.RegisterKnownProviders()

	// Each known provider must get its own client.
	for name := range KnownProviderPresets() {
		client := m.ForProvider(name)
		if client == m.defaultClient {
			t.Errorf("provider %q should have a dedicated client, got defaultClient", name)
		}
	}

	// Unknown provider still falls back to default.
	if m.ForProvider("unknown-provider") != m.defaultClient {
		t.Error("unknown provider should return defaultClient")
	}
}

func TestRegisterKnownProviders_PoolIsolation(t *testing.T) {
	m := NewDefault()
	m.RegisterKnownProviders()

	oaiTransport := m.Pool("openai").Transport()
	antTransport := m.Pool("anthropic").Transport()

	// Transports must be different instances.
	if oaiTransport == antTransport {
		t.Error("openai and anthropic must have different transports")
	}

	// Verify preset values applied.
	oaiPreset := KnownProviderPresets()["openai"]
	if oaiTransport.MaxIdleConnsPerHost != oaiPreset.MaxIdleConnsPerHost {
		t.Errorf("openai MaxIdleConnsPerHost = %d, want %d",
			oaiTransport.MaxIdleConnsPerHost, oaiPreset.MaxIdleConnsPerHost)
	}

	antPreset := KnownProviderPresets()["anthropic"]
	if antTransport.MaxIdleConnsPerHost != antPreset.MaxIdleConnsPerHost {
		t.Errorf("anthropic MaxIdleConnsPerHost = %d, want %d",
			antTransport.MaxIdleConnsPerHost, antPreset.MaxIdleConnsPerHost)
	}
}

func TestPool(t *testing.T) {
	m := NewDefault()
	m.RegisterKnownProviders()

	// Known provider pool.
	pool := m.Pool("openai")
	if pool.Name() != "openai" {
		t.Errorf("pool name = %q, want %q", pool.Name(), "openai")
	}
	if pool.Client() == nil {
		t.Error("pool client must not be nil")
	}
	if pool.StreamClient() == nil {
		t.Error("pool stream client must not be nil")
	}
	if pool.Transport() == nil {
		t.Error("pool transport must not be nil")
	}
	if pool.Client() == m.defaultClient {
		t.Error("known provider pool must use dedicated client")
	}

	// Unknown provider pool should fallback to defaults.
	unknownPool := m.Pool("mystery-ai")
	if unknownPool.Client() != m.defaultClient {
		t.Error("unknown provider pool should use defaultClient")
	}
}

func BenchmarkForProvider_KnownProviders(b *testing.B) {
	m := NewDefault()
	m.RegisterKnownProviders()

	providers := []string{"openai", "anthropic", "gemini", "groq", "unknown"}

	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			_ = m.ForProvider(providers[i%len(providers)])
			i++
		}
	})
}
