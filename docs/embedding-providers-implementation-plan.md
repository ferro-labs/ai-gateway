# Embedding Providers Implementation Plan

This document tracks the implementation plan and final status for making
`/v1/embeddings` support consistent across built-in providers that have text
embedding support in the model catalog or expose an embedding-capable API.

## Goal

Expand `core.EmbeddingProvider` coverage so the gateway can route OpenAI-style
`POST /v1/embeddings` requests to every supported built-in text embedding
provider, with normalized request validation, response mapping, token usage,
registry capabilities, tests, and model visibility.

## Current State

The gateway has a normalized embedding interface:

- `core.EmbeddingProvider`
- `core.EmbeddingRequest`
- `core.EmbeddingResponse`
- `Gateway.Embed`
- `cmd/ferrogw` route: `POST /v1/embeddings`

Providers that implement and advertise `CapabilityEmbed`:

| Provider ID | Status | Notes |
|---|---|---|
| `openai` | Implemented | Uses OpenAI embeddings API and has tests. |
| `cloudflare` | Implemented | Uses Workers AI `/embeddings` adapter and has tests. |
| `hugging-face` | Implemented | Uses `/embeddings` adapter and has tests. |
| `cohere` | Implemented | Uses Cohere `/v1/embed`; dedicated `Embed` tests added. |
| `bedrock` | Implemented for text | Supports Titan text and Cohere embedding model families; image/multimodal families are deferred. |
| `databricks` | Implemented | Uses OpenAI-compatible model-serving embeddings endpoint. |
| `fireworks` | Implemented | Uses OpenAI-compatible `/v1/embeddings`. |
| `gemini` | Implemented for text | Uses native Gemini `batchEmbedContents`. |
| `mistral` | Implemented | Uses OpenAI-compatible `/v1/embeddings`. |
| `novita` | Implemented | Uses OpenAI-compatible `/embeddings` under Novita's `/openai/v1` base. |
| `together` | Implemented | Uses OpenAI-compatible `/v1/embeddings`. |
| `vertex-ai` | Implemented for text | Uses Vertex AI publisher model `:predict`; multimodal embeddings are deferred. |

Catalog entries also exist for non-built-in or differently named providers such
as `azure`, `voyage`, `volcengine`, `vercel_ai_gateway`, `github_copilot`,
`gigachat`, and `llamagate`. Those remain out of scope unless corresponding
built-in provider packages are added or provider IDs are normalized first.

## Principles

- End users keep using the gateway's OpenAI-compatible `/v1/embeddings` route.
- Provider implementations adapt to each upstream API internally.
- Only advertise `CapabilityEmbed` after a provider has a working `Embed`
  method and tests.
- Keep `SupportsModel` precise enough that embedding requests do not get routed
  to providers that only support similarly named chat models.
- Add compile-time assertions for every new implementation:
  `var _ core.EmbeddingProvider = (*Provider)(nil)`.
- Keep catalog and `SupportedModels()` behavior aligned enough for `/v1/models`
  to expose common embedding models.
- Use provider-specific tests with `httptest` or fake clients; do not require
  real API keys.

## Completed Cross-Cutting Work

1. Added package-local tests for new and hardened embedding providers covering:
   request path/auth, string input, `[]string` input, invalid input, empty input,
   upstream non-200 errors, response vector/index mapping, and token usage.
2. Added a registry consistency test:
   every provider advertising `CapabilityEmbed` must implement
   `core.EmbeddingProvider`, and every provider implementing
   `core.EmbeddingProvider` should advertise `CapabilityEmbed`.
3. Hardened Cohere embedding behavior so unsafe `[]interface{}` values now
   return a clear error instead of silently dropping non-string entries.
4. Added embedding model IDs to `SupportedModels()` where provider lists were
   chat-focused.

## Provider Implementation Status

### Cohere

Status: complete.

- Added dedicated `Embed` tests for `/v1/embed`, Bearer auth, success mapping,
  string and array input, invalid input, empty input, unsafe `[]interface{}`,
  token usage, and upstream errors.
- Added common Cohere embedding models such as `embed-v4.0`,
  `embed-english-v3.0`, and `embed-multilingual-v3.0` to `SupportedModels()`.

### OpenAI-compatible providers

Status: complete.

| Provider | Endpoint |
|---|---|
| `mistral` | `POST {baseURL}/v1/embeddings` |
| `together` | `POST {baseURL}/v1/embeddings` |
| `fireworks` | `POST {baseURL}/v1/embeddings` |
| `novita` | `POST {baseURL}/embeddings` |
| `databricks` | `POST {normalizedBaseURL}/embeddings` |

Each provider supports string and array inputs, maps OpenAI-style embedding
responses into `core.EmbeddingResponse`, advertises `CapabilityEmbed`, and has
package-local tests for success and error paths.

### Gemini

Status: complete for text embeddings.

- Uses `POST /v1beta/models/{model}:batchEmbedContents?key=...`.
- Maps string and array text input into Gemini content parts.
- Supports `dimensions` through `outputDimensionality`.
- Maps Gemini embedding values and token metadata into
  `core.EmbeddingResponse`.

### Vertex AI

Status: complete for text embeddings.

- Uses `POST /v1/projects/{project}/locations/{region}/publishers/google/models/{model}:predict`.
- Supports `text-embedding-*`, `textembedding-gecko*`,
  `text-multilingual-embedding-*`, and `gemini-embedding-001` text embedding
  model families.
- Supports API-key and service-account authorization paths through existing
  provider auth helpers.
- Defers multimodal embedding models because `core.EmbeddingRequest` currently
  models text input only.

### Bedrock

Status: complete for text embeddings.

- Uses the existing Bedrock Runtime `InvokeModel` client.
- Supports `amazon.titan-embed-text-*` text embedding models.
- Supports `cohere.embed-*` text embedding models, including Cohere v4 float
  embedding responses.
- Defers Titan image, Nova multimodal, TwelveLabs, and other non-text embedding
  families until the core embedding request supports non-text inputs.

## Validation Plan

Run after provider changes:

```bash
go test ./providers/<provider>/...
go test ./providers/...
```

Run before merging the full feature:

```bash
go test ./...
make build
git diff --check
```

## Open Questions

- Should gateway discovery results rebuild embedding indexes after discovery
  updates? Today discovery updates `AllModels()` output, but model indexes are
  built from provider `SupportedModels()`.
- Should `core.EmbeddingRequest` grow multimodal input support before enabling
  image/multimodal embedding models from Bedrock or Vertex AI?
- Should catalog provider key `vertex_ai` be normalized to the runtime provider
  ID `vertex-ai` for consistent cost lookup?
- Should `azure` catalog embedding models be mapped to `azure-openai`, or should
  Azure OpenAI embedding support be planned separately?

## Definition of Done

- Every built-in provider with supported text embedding models in the catalog
  implements `EmbeddingProvider`.
- Registry `CapabilityEmbed` matches actual interface implementation.
- `/v1/embeddings` routes correctly for each implemented provider.
- Tests cover success, invalid input, and upstream error paths for every
  implemented provider.
- Catalog, provider model lists, and routing behavior are consistent enough that
  users can discover and call embedding models without provider-specific request
  shapes.
