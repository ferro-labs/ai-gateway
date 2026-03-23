package transport

import (
	"net/http"
	"time"
)

// ProviderPool holds a per-provider HTTP client and its streaming counterpart.
// Each pool is fully isolated — one provider's slow responses or connection
// exhaustion cannot degrade other providers.
type ProviderPool struct {
	name      string
	client    *http.Client
	streamCli *http.Client
	cfg       Config
}

// ProviderPreset contains tuned transport settings for a known provider.
// These are based on observed provider behaviour in production.
type ProviderPreset struct {
	// MaxIdleConnsPerHost controls the idle connection pool size.
	// High-traffic providers (OpenAI, Anthropic) benefit from larger pools.
	MaxIdleConnsPerHost int

	// ResponseHeaderTimeout is the maximum time to wait for response headers.
	// Some providers (Bedrock, Vertex) have higher cold-start latency.
	ResponseHeaderTimeout time.Duration

	// DialTimeout overrides the default dial timeout.
	DialTimeout time.Duration
}

// KnownProviderPresets returns tuned transport settings for high-traffic
// providers. Providers not in this map use DefaultConfig().
//
// These presets are informed by production traffic patterns:
//   - OpenAI/Azure: highest traffic, needs largest pools
//   - Anthropic: large prompts → higher header timeout
//   - Bedrock/Vertex: cloud-native cold starts → higher dial+header timeout
//   - Ollama: local, low latency → smaller pools
func KnownProviderPresets() map[string]ProviderPreset {
	return map[string]ProviderPreset{
		"openai": {
			MaxIdleConnsPerHost:   200,
			ResponseHeaderTimeout: 30 * time.Second,
		},
		"azure-openai": {
			MaxIdleConnsPerHost:   200,
			ResponseHeaderTimeout: 30 * time.Second,
		},
		"anthropic": {
			MaxIdleConnsPerHost:   150,
			ResponseHeaderTimeout: 60 * time.Second,
		},
		"gemini": {
			MaxIdleConnsPerHost:   100,
			ResponseHeaderTimeout: 30 * time.Second,
		},
		"bedrock": {
			MaxIdleConnsPerHost:   100,
			ResponseHeaderTimeout: 120 * time.Second,
			DialTimeout:           15 * time.Second,
		},
		"vertex-ai": {
			MaxIdleConnsPerHost:   100,
			ResponseHeaderTimeout: 120 * time.Second,
			DialTimeout:           15 * time.Second,
		},
		"groq": {
			MaxIdleConnsPerHost:   100,
			ResponseHeaderTimeout: 15 * time.Second,
		},
		"ollama": {
			MaxIdleConnsPerHost:   20,
			ResponseHeaderTimeout: 120 * time.Second,
			DialTimeout:           5 * time.Second,
		},
	}
}

// applyPreset merges a ProviderPreset into a base Config.
// Zero-valued preset fields are left at the base config defaults.
func applyPreset(base Config, preset ProviderPreset) Config {
	if preset.MaxIdleConnsPerHost > 0 {
		base.MaxIdleConnsPerHost = preset.MaxIdleConnsPerHost
	}
	if preset.ResponseHeaderTimeout > 0 {
		base.ResponseHeaderTimeout = preset.ResponseHeaderTimeout
	}
	if preset.DialTimeout > 0 {
		base.DialTimeout = preset.DialTimeout
	}
	return base
}

// RegisterKnownProviders registers isolated pools for all providers in the
// KnownProviderPresets map. Call once at startup after creating the Manager.
func (m *Manager) RegisterKnownProviders() {
	for name, preset := range KnownProviderPresets() {
		cfg := applyPreset(m.cfg, preset)
		m.RegisterProvider(name, cfg)
	}
}

// Pool returns the ProviderPool for a named provider.
// If the provider has no dedicated pool, returns a pool backed by the
// default client. The returned pool is safe for concurrent use.
func (m *Manager) Pool(provider string) *ProviderPool {
	m.mu.RLock()
	c, ok := m.providers[provider]
	m.mu.RUnlock()

	if !ok {
		return &ProviderPool{
			name:      provider,
			client:    m.defaultClient,
			streamCli: m.streamClient,
			cfg:       m.cfg,
		}
	}

	return &ProviderPool{
		name:      provider,
		client:    c,
		streamCli: m.streamClient,
		cfg:       m.cfg,
	}
}

// Client returns the non-streaming HTTP client for this provider.
func (pp *ProviderPool) Client() *http.Client {
	return pp.client
}

// StreamClient returns the SSE-optimized HTTP client for this provider.
func (pp *ProviderPool) StreamClient() *http.Client {
	return pp.streamCli
}

// Name returns the provider name this pool is associated with.
func (pp *ProviderPool) Name() string {
	return pp.name
}

// Transport returns the underlying *http.Transport for inspection
// (e.g. connection pool stats).
func (pp *ProviderPool) Transport() *http.Transport {
	return pp.client.Transport.(*http.Transport)
}
