// Package providers re-exports all contracts and types from providers/core
// as type aliases so that existing code importing this package continues to
// compile without any changes.
//
// New code and provider sub-packages should import providers/core directly.
package providers

import "github.com/ferro-labs/ai-gateway/providers/core"

// ---------------------------------------------------------------- Interfaces -

// Provider is an alias for core.Provider.
type Provider = core.Provider

// ProviderUnwrapper is an alias for core.ProviderUnwrapper.
type ProviderUnwrapper = core.ProviderUnwrapper

// StreamProvider is an alias for core.StreamProvider.
type StreamProvider = core.StreamProvider

// ProxiableProvider is an alias for core.ProxiableProvider.
type ProxiableProvider = core.ProxiableProvider

// NonOpenAIWireProvider is an alias for core.NonOpenAIWireProvider.
type NonOpenAIWireProvider = core.NonOpenAIWireProvider

// RequestSigner is an alias for core.RequestSigner.
type RequestSigner = core.RequestSigner

// EmbeddingProvider is an alias for core.EmbeddingProvider.
type EmbeddingProvider = core.EmbeddingProvider

// ImageProvider is an alias for core.ImageProvider.
type ImageProvider = core.ImageProvider

// RerankProvider is an alias for core.RerankProvider.
type RerankProvider = core.RerankProvider

// ModerationProvider is an alias for core.ModerationProvider.
type ModerationProvider = core.ModerationProvider

// TranscriptionProvider is an alias for core.TranscriptionProvider.
type TranscriptionProvider = core.TranscriptionProvider

// SpeechProvider is an alias for core.SpeechProvider.
type SpeechProvider = core.SpeechProvider

// BatchProvider is an alias for core.BatchProvider.
type BatchProvider = core.BatchProvider

// DiscoveryProvider is an alias for core.DiscoveryProvider.
type DiscoveryProvider = core.DiscoveryProvider

// ConfiguredModelProvider is an alias for core.ConfiguredModelProvider.
type ConfiguredModelProvider = core.ConfiguredModelProvider

// AnyModelProvider is an alias for core.AnyModelProvider.
type AnyModelProvider = core.AnyModelProvider

// ProviderSource is an alias for core.ProviderSource.
type ProviderSource = core.ProviderSource

// ------------------------------------------------------------------- Types --

// Request is an alias for core.Request.
type Request = core.Request

// Response is an alias for core.Response.
type Response = core.Response

// Message is an alias for core.Message.
type Message = core.Message

// Choice is an alias for core.Choice.
type Choice = core.Choice

// StreamChunk is an alias for core.StreamChunk.
type StreamChunk = core.StreamChunk

// StreamChoice is an alias for core.StreamChoice.
type StreamChoice = core.StreamChoice

// MessageDelta is an alias for core.MessageDelta.
type MessageDelta = core.MessageDelta

// StreamNormalizer is an alias for core.StreamNormalizer.
type StreamNormalizer = core.StreamNormalizer

// Usage is an alias for core.Usage.
type Usage = core.Usage

// ModelInfo is an alias for core.ModelInfo.
type ModelInfo = core.ModelInfo

// ContentPart is an alias for core.ContentPart.
type ContentPart = core.ContentPart

// ImageURLPart is an alias for core.ImageURLPart.
type ImageURLPart = core.ImageURLPart

// Tool is an alias for core.Tool.
type Tool = core.Tool

// Function is an alias for core.Function.
type Function = core.Function

// ToolCall is an alias for core.ToolCall.
type ToolCall = core.ToolCall

// FunctionCall is an alias for core.FunctionCall.
type FunctionCall = core.FunctionCall

// ResponseFormat is an alias for core.ResponseFormat.
type ResponseFormat = core.ResponseFormat

// EmbeddingRequest is an alias for core.EmbeddingRequest.
type EmbeddingRequest = core.EmbeddingRequest

// EmbeddingResponse is an alias for core.EmbeddingResponse.
type EmbeddingResponse = core.EmbeddingResponse

// Embedding is an alias for core.Embedding.
type Embedding = core.Embedding

// EmbeddingUsage is an alias for core.EmbeddingUsage.
type EmbeddingUsage = core.EmbeddingUsage

// ImageRequest is an alias for core.ImageRequest.
type ImageRequest = core.ImageRequest

// ImageResponse is an alias for core.ImageResponse.
type ImageResponse = core.ImageResponse

// RerankRequest is an alias for core.RerankRequest.
type RerankRequest = core.RerankRequest

// RerankResponse is an alias for core.RerankResponse.
type RerankResponse = core.RerankResponse

// RerankResult is an alias for core.RerankResult.
type RerankResult = core.RerankResult

// ModerationRequest is an alias for core.ModerationRequest.
type ModerationRequest = core.ModerationRequest

// ModerationResponse is an alias for core.ModerationResponse.
type ModerationResponse = core.ModerationResponse

// TranscriptionRequest is an alias for core.TranscriptionRequest.
type TranscriptionRequest = core.TranscriptionRequest

// TranscriptionResponse is an alias for core.TranscriptionResponse.
type TranscriptionResponse = core.TranscriptionResponse

// SpeechRequest is an alias for core.SpeechRequest.
type SpeechRequest = core.SpeechRequest

// SpeechResponse is an alias for core.SpeechResponse.
type SpeechResponse = core.SpeechResponse

// GeneratedImage is an alias for core.GeneratedImage.
type GeneratedImage = core.GeneratedImage

// ---------------------------------------------------------------- Constants --

// Role constants — re-exported from core.
const (
	RoleUser      = core.RoleUser
	RoleAssistant = core.RoleAssistant
	RoleSystem    = core.RoleSystem
	RoleTool      = core.RoleTool
	RoleDeveloper = core.RoleDeveloper

	ContentTypeText = core.ContentTypeText
	SSEDone         = core.SSEDone
)

// ----------------------------------------------------------------- Functions -

// UnsupportedParamError re-exports core.UnsupportedParamError.
type UnsupportedParamError = core.UnsupportedParamError

// ErrProviderSaturated re-exports core.ErrProviderSaturated.
var ErrProviderSaturated = core.ErrProviderSaturated

// ParseStatusCode re-exports core.ParseStatusCode.
var ParseStatusCode = core.ParseStatusCode

// RetryAfterFrom re-exports core.RetryAfterFrom.
var RetryAfterFrom = core.RetryAfterFrom

// WithName re-exports core.WithName.
var WithName = core.WithName

// As resolves an optional capability through identity-only provider wrappers.
func As[T any](p Provider) (T, bool) {
	return core.As[T](p)
}
