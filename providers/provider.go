// Package providers defines the Provider interface and shared data types
// used across all LLM provider implementations.
//
// All interfaces, types, and constants are defined in providers/core and
// re-exported here as type aliases for backwards compatibility. See
// providers/facade_aliases.go for the alias declarations.
//
// The Provider interface must be implemented by any backend that integrates
// with the gateway. StreamProvider extends Provider for streaming responses.
//
// Core types: Request, Response, Message, StreamChunk, ModelInfo.
package providers
