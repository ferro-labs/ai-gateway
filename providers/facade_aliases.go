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

// StreamProvider is an alias for core.StreamProvider.
type StreamProvider = core.StreamProvider

// ProxiableProvider is an alias for core.ProxiableProvider.
type ProxiableProvider = core.ProxiableProvider

// EmbeddingProvider is an alias for core.EmbeddingProvider.
type EmbeddingProvider = core.EmbeddingProvider

// ImageProvider is an alias for core.ImageProvider.
type ImageProvider = core.ImageProvider

// DiscoveryProvider is an alias for core.DiscoveryProvider.
type DiscoveryProvider = core.DiscoveryProvider

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

// GeneratedImage is an alias for core.GeneratedImage.
type GeneratedImage = core.GeneratedImage

// ---------------------------------------------------------------- Constants --

// Role constants — re-exported from core.
const (
	RoleUser      = core.RoleUser
	RoleAssistant = core.RoleAssistant
	RoleSystem    = core.RoleSystem
	RoleTool      = core.RoleTool

	ContentTypeText = core.ContentTypeText
	SSEDone         = core.SSEDone
)

// ----------------------------------------------------------------- Functions -

// ParseStatusCode re-exports core.ParseStatusCode.
var ParseStatusCode = core.ParseStatusCode
