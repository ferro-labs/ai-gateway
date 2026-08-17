// Package core defines the stable public contracts for the providers layer:
// interfaces, shared data types, and supporting helpers.
//
// Provider implementations and consumer packages should import this package for
// type definitions rather than the root providers package when operating from a
// sub-package context.
//
// The root providers package re-exports everything here as type aliases so
// existing code using providers.Provider, providers.Request, etc. continues
// to compile without changes.
package core

import (
	"context"
	"net/http"
)

// Provider defines the interface that all LLM providers must implement.
//
// A provider does not report a model list. Which models it serves is answered
// by the model catalog and, for providers implementing DiscoveryProvider, by
// live enumeration of the upstream /models endpoint — both of which stay
// current without a gateway release. A hardcoded list inside the provider went
// stale the day it was written and shadowed the two sources that did not.
type Provider interface {
	Name() string
	Complete(ctx context.Context, req Request) (*Response, error)

	// SupportsModel is ADVISORY. It does not decide whether the gateway routes
	// a model here — the routing index does, from the catalog, live discovery
	// and whatever this instance's config named. Returning true for everything
	// buys a provider no traffic; it only answers callers that have no index of
	// their own, such as the pass-through proxy resolving a raw /v1/* body.
	//
	// To serve model ids no index can enumerate, implement AnyModelProvider.
	// That is a separate, deliberate declaration precisely so it cannot be
	// acquired by writing `return true` here.
	SupportsModel(model string) bool
}

// ProviderUnwrapper exposes the provider beneath an identity-only decorator.
// Consumers should normally use As rather than unwrapping directly so nested
// decorators remain an implementation detail.
type ProviderUnwrapper interface {
	UnwrapProvider() Provider
}

// IdentityUnwrapper exposes the provider whose vendor identity a decorator
// carries. It is for decorators that change BEHAVIOUR but not identity — the
// routing layer's circuit breaker, concurrency limiter and model-index view.
//
// It is deliberately a separate interface from ProviderUnwrapper rather than a
// second implementation of it, because the two answer different questions and
// only one of them is safe to make transparent:
//
//   - As walks ProviderUnwrapper to resolve capability interfaces. A
//     behaviour-adding decorator must NOT be transparent there, or a caller
//     resolves StreamProvider/EmbeddingProvider straight past the circuit
//     breaker and concurrency limiter that were wrapped around it, silently
//     losing the protection.
//   - CanonicalName walks both, because identity is safe to see through: none
//     of those decorators claims a vendor of its own, and pricing has to reach
//     the vendor the catalog is keyed on.
//
// Implement this on any decorator that wraps a provider without becoming a
// different vendor. Without it, CanonicalName stops at the decorator and
// returns whatever Name() promotes to — which for an aliased registration is
// the routing alias, so the catalog lookup silently finds nothing.
type IdentityUnwrapper interface {
	UnwrapIdentity() Provider
}

type namedProvider struct {
	Provider
	name string
}

var _ Provider = (*namedProvider)(nil)

func (p *namedProvider) Name() string { return p.name }

func (p *namedProvider) UnwrapProvider() Provider { return p.Provider }

// WithName returns a provider registered under name without copying its
// optional capability interfaces onto a lossy wrapper. Use As to resolve an
// optional interface through the returned identity decorator.
func WithName(p Provider, name string) Provider {
	if p == nil || name == "" || p.Name() == name {
		return p
	}
	return &namedProvider{Provider: p, name: name}
}

// CanonicalName returns the identity beneath any WithName decorator: the
// vendor name the model catalog and price book are keyed on. A provider
// registered under its own name returns its own Name(); one registered under a
// routing alias unwraps to the vendor identity beneath the alias. The bounded
// walk mirrors As and cannot hang on a self-referential wrapper.
// It walks IdentityUnwrapper as well as ProviderUnwrapper. The routing layer
// hands strategies a provider wrapped in the model-index view, the concurrency
// limiter and the circuit breaker; none of those is a vendor, and stopping at
// them returned the routing alias, which the catalog cannot price.
func CanonicalName(p Provider) string {
	for range 32 {
		var next Provider
		switch d := p.(type) {
		case ProviderUnwrapper:
			next = d.UnwrapProvider()
		case IdentityUnwrapper:
			next = d.UnwrapIdentity()
		default:
			return p.Name()
		}
		if next == nil {
			return p.Name()
		}
		p = next
	}
	return p.Name()
}

// As resolves T from p or from an identity decorator beneath it. Every provider
// in the chain is inspected, including the one reached by the final unwrap. The
// bounded walk (at most 32 unwraps) prevents a broken third-party decorator that
// unwraps to itself from hanging capability resolution.
func As[T any](p Provider) (T, bool) {
	var zero T
	for i := 0; ; i++ {
		if capability, ok := any(p).(T); ok {
			return capability, true
		}
		unwrapper, ok := p.(ProviderUnwrapper)
		if !ok || i >= 32 {
			return zero, false
		}
		next := unwrapper.UnwrapProvider()
		if next == nil {
			return zero, false
		}
		p = next
	}
}

// AnyModelProvider is the opt-in declaration that a provider's upstream accepts
// model ids nothing can enumerate in advance — a deployment name chosen at
// deploy time, a serving endpoint named by a workspace, a model pulled onto the
// operator's own machine. Neither a public catalog nor a /models call can be
// complete for these, so the routing index alone would refuse every one.
//
// It is deliberately a second method rather than a value SupportsModel returns.
// Twelve providers answered SupportsModel with `return true` because it was the
// shortest thing that compiled, and the result was that an unknown model name
// sent the prompt body to three of them before returning 404. A declaration a
// new provider's author has to write on purpose cannot be inherited by accident.
//
// It does NOT outrank the index. A model the index has an owner for is routed to
// that owner and never offered here, so declaring this cannot take traffic away
// from the provider that actually serves a model — it only extends the reach of
// a target to names nobody claims. That is the same specificity ordering LiteLLM
// applies when an exact model_list entry and a wildcard entry both match.
//
// Declaring it means: every request for a model no target owns is offered to
// this provider, in configured target order, prompt body included. Do not
// declare it for an upstream whose models a catalog entry or DiscoveryProvider
// could cover instead.
type AnyModelProvider interface {
	Provider
	// ServesAnyModel is a compile-time marker with no behavior.
	ServesAnyModel()
}

// ConfiguredModelProvider is the optional interface for a provider whose
// routable models come from this instance's own configuration rather than from
// the catalog: an Azure OpenAI deployment name, the OLLAMA_MODELS list pointing
// at a local server. No public catalog can know these, so they are the one kind
// of model list a provider is still the authority on.
//
// Returning nil is the normal case and means "ask the catalog".
type ConfiguredModelProvider interface {
	Provider
	ConfiguredModels() []string
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

// NonOpenAIWireProvider is an optional marker for a ProxiableProvider whose
// upstream cannot serve a transparently-forwarded OpenAI-shaped request at its
// base URL — either because its request/response shape is not OpenAI-compatible
// (Anthropic Messages, Google Gemini, AWS Bedrock, Cohere) or because it needs
// non-standard path/auth rewriting (Azure OpenAI/Foundry deployment paths,
// Vertex AI publisher-prefixed models).
//
// The transparent /v1/* pass-through proxy refuses these providers with 501
// rather than forwarding a request their upstream cannot parse; they remain
// fully usable through their native translated chat/embeddings/images
// endpoints. A provider graduates to pass-through additively: via a separate
// OpenAI-compatible provider entry, by implementing RequestSigner, or via a
// future request-rewriter seam.
type NonOpenAIWireProvider interface {
	Provider
	// NonOpenAIWire is a compile-time marker with no behavior.
	NonOpenAIWire()
}

// RequestSigner is an optional interface for a ProxiableProvider whose upstream
// requires per-request signing that cannot be expressed as static AuthHeaders
// (e.g. AWS SigV4). When a provider implements it, the pass-through proxy signs
// each outbound request before sending it upstream; a signing failure is
// surfaced as an upstream error rather than forwarding an unsigned request.
type RequestSigner interface {
	// SignProxyRequest signs the fully-formed outbound proxy request in place.
	// An implementation that reads req.Body to compute the signature (e.g. AWS
	// SigV4 body hashing) must restore it before returning — replace req.Body
	// with a fresh io.NopCloser over the buffered bytes — so the base transport
	// forwards the request with an intact body.
	SignProxyRequest(req *http.Request) error
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

// RerankProvider is an optional interface for providers that support the
// /v1/rerank endpoint (Cohere-lineage reranking). The gateway-facing shape is
// the Cohere v2 rerank contract; adapters translate their native shape onto it.
type RerankProvider interface {
	Provider
	Rerank(ctx context.Context, req RerankRequest) (*RerankResponse, error)
}

// ModerationProvider is an optional interface for providers that support the
// /v1/moderations endpoint. The gateway-facing shape is the OpenAI moderations
// contract.
type ModerationProvider interface {
	Provider
	Moderate(ctx context.Context, req ModerationRequest) (*ModerationResponse, error)
}

// TranscriptionProvider is an optional interface for providers that support the
// /v1/audio/transcriptions and /v1/audio/translations endpoints (speech-to-text).
// The gateway-facing shape is the OpenAI audio contract.
type TranscriptionProvider interface {
	Provider
	Transcribe(ctx context.Context, req TranscriptionRequest) (*TranscriptionResponse, error)
}

// SpeechProvider is an optional interface for providers that support the
// /v1/audio/speech endpoint (text-to-speech). The gateway-facing shape is the
// OpenAI speech contract; an adapter returns decoded audio bytes regardless of
// whether the upstream streams raw audio or wraps it in base64-in-JSON.
type SpeechProvider interface {
	Provider
	Speech(ctx context.Context, req SpeechRequest) (*SpeechResponse, error)
}

// BatchProvider is an optional interface for providers that expose the OpenAI
// Files (/v1/files) and Batch (/v1/batches) APIs for transparent pass-through.
// These endpoints carry no model and reference opaque provider-scoped ids, so
// the gateway forwards them to a single configured batch target rather than
// routing by model — BatchBaseURL is the root the operation path hangs beneath
// (usually the OpenAI-compatible base, but Azure serves batch at a different
// root than its chat surface), and BatchAuthHeaders authenticates the forward.
type BatchProvider interface {
	Provider
	BatchBaseURL() string
	BatchAuthHeaders() map[string]string
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
	// ModelsFor reports the models one provider serves, composed from every
	// source that knows: the catalog, live discovery, and whatever the
	// instance's own config named. Callers must not ask the Provider — it no
	// longer keeps a list, and the composition is what /v1/models answers with.
	ModelsFor(name string) []ModelInfo
	AllModels() []ModelInfo
	FindByModel(model string) (Provider, bool)
}
