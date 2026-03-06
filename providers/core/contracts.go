// Package core defines the stable public contracts for the providers layer:
// interfaces, shared data types, and supporting helpers.
//
// All provider implementations and consumer packages (gateway, admin, ferrocloud)
// should import this package for type definitions rather than the root
// providers package when operating from a sub-package context.
//
// The root providers package re-exports everything here as type aliases so
// existing code using providers.Provider, providers.Request, etc. continues
// to compile without changes.
package core

import "context"

// Provider defines the interface that all LLM providers must implement.
type Provider interface {
	Name() string
	Complete(ctx context.Context, req Request) (*Response, error)
	SupportedModels() []string
	SupportsModel(model string) bool
	Models() []ModelInfo
}

// StreamProvider is an optional interface for providers that support streaming.
type StreamProvider interface {
	Provider
	CompleteStream(ctx context.Context, req Request) (<-chan StreamChunk, error)
}

// ProxiableProvider is an optional interface for providers that support
// raw HTTP proxy pass-through. The gateway uses this to forward requests
// for endpoints it does not handle natively (e.g. /v1/files, /v1/batches).
type ProxiableProvider interface {
	Provider
	// BaseURL returns the provider's root API URL (no trailing slash).
	BaseURL() string
	// AuthHeaders returns the HTTP headers required to authenticate with the
	// provider (e.g. {"Authorization": "Bearer sk-..."}).
	AuthHeaders() map[string]string
}

// EmbeddingProvider is an optional interface for providers that support
// the /v1/embeddings endpoint.
type EmbeddingProvider interface {
	Provider
	Embed(ctx context.Context, req EmbeddingRequest) (*EmbeddingResponse, error)
}

// ImageProvider is an optional interface for providers that support
// the /v1/images/generations endpoint.
type ImageProvider interface {
	Provider
	GenerateImage(ctx context.Context, req ImageRequest) (*ImageResponse, error)
}

// DiscoveryProvider is an optional interface for providers that can
// enumerate their available models live from the provider API.
type DiscoveryProvider interface {
	Provider
	DiscoverModels(ctx context.Context) ([]ModelInfo, error)
}

// ProviderSource is a read-only view over a collection of registered providers.
// Both *Registry and *Gateway implement this interface, enabling registry
// consolidation: handlers that only need to read provider info can accept
// a ProviderSource instead of a concrete *Registry.
type ProviderSource interface {
	Get(name string) (Provider, bool)
	List() []string
	AllModels() []ModelInfo
	FindByModel(model string) (Provider, bool)
}
