# Providers

The gateway speaks to **30 LLM providers** behind one OpenAI-compatible API. Each
provider lives in its own subpackage (`providers/<id>/`) and implements a small
set of Go interfaces from [`core`](core/contracts.go); the gateway translates
every request and response to and from the OpenAI shape, so a client written
against the OpenAI SDK reaches any of them unchanged.

This file is the **capability matrix** — which endpoints each provider actually
implements — kept honest by `providers.TestEndpointSupportMatchesCode`, which
fails the build if a provider's real interface set ever drifts from this table.

## Legend

| Mark | Meaning |
|:--:|---|
| ✅ | Implemented — a typed surface (`Complete`, `Embed`, `Rerank`, …), or a working `/v1/*` pass-through for **Proxy** |
| ➖ | Not implemented (the vendor does not offer it, or it is a documented gap) |
| ⛔ | **Proxy only:** the provider speaks a native (non-OpenAI) wire, so the transparent `/v1/*` pass-through returns **501** by design — reach it through the translated surfaces above |

Every provider implements **Chat** (`core.Provider`) by definition, so it is not a
column. "Wire" is how the gateway talks to the upstream: `openai` = an
OpenAI-compatible HTTP surface the shared translator drives; `native` = a
vendor-specific wire with a bespoke adapter (Anthropic Messages, Gemini
`generateContent`, Cohere v2, Bedrock InvokeModel, Vertex, Replicate).

## Capability matrix

| Provider | Wire | Chat | Stream | Embeddings | Images | Rerank | Moderation | Transcribe (STT) | Speech (TTS) | Batch | Discovery | Proxy |
|---|:--:|:--:|:--:|:--:|:--:|:--:|:--:|:--:|:--:|:--:|:--:|:--:|
| `ai21` | openai | ✅ | ✅ | ➖ | ➖ | ➖ | ➖ | ➖ | ➖ | ➖ | ➖ | ✅ |
| `anthropic` | native | ✅ | ✅ | ➖ | ➖ | ➖ | ➖ | ➖ | ➖ | ➖ | ✅ | ⛔ |
| `azure-foundry` | openai | ✅ | ✅ | ✅ | ➖ | ➖ | ➖ | ➖ | ➖ | ➖ | ➖ | ⛔ |
| `azure-openai` | openai | ✅ | ✅ | ✅ | ✅ | ➖ | ➖ | ✅ | ✅ | ✅ | ➖ | ⛔ |
| `bedrock` | native | ✅ | ✅ | ✅ | ✅ | ✅ | ➖ | ➖ | ➖ | ➖ | ➖ | ⛔ |
| `cerebras` | openai | ✅ | ✅ | ➖ | ➖ | ➖ | ➖ | ➖ | ➖ | ➖ | ✅ | ✅ |
| `cloudflare` | openai | ✅ | ✅ | ✅ | ➖ | ➖ | ➖ | ➖ | ➖ | ➖ | ➖ | ✅ |
| `cohere` | native | ✅ | ✅ | ✅ | ➖ | ✅ | ➖ | ➖ | ➖ | ➖ | ➖ | ⛔ |
| `databricks` | openai | ✅ | ✅ | ✅ | ➖ | ➖ | ➖ | ➖ | ➖ | ➖ | ➖ | ✅ |
| `deepinfra` | openai | ✅ | ✅ | ✅ | ✅ | ✅ | ➖ | ✅ | ✅ | ➖ | ✅ | ✅ |
| `deepseek` | openai | ✅ | ✅ | ➖ | ➖ | ➖ | ➖ | ➖ | ➖ | ➖ | ✅ | ✅ |
| `fireworks` | openai | ✅ | ✅ | ✅ | ➖ | ➖ | ➖ | ✅ | ➖ | ➖ | ✅ | ✅ |
| `gemini` | native | ✅ | ✅ | ✅ | ✅ | ➖ | ➖ | ➖ | ➖ | ➖ | ➖ | ⛔ |
| `groq` | openai | ✅ | ✅ | ➖ | ➖ | ➖ | ➖ | ✅ | ✅ | ✅ | ✅ | ✅ |
| `hugging-face` | openai | ✅ | ✅ | ✅ | ✅ | ➖ | ➖ | ➖ | ➖ | ➖ | ✅ | ✅ |
| `mistral` | openai | ✅ | ✅ | ✅ | ➖ | ➖ | ✅ | ✅ | ✅ | ➖ | ✅ | ✅ |
| `moonshot` | openai | ✅ | ✅ | ➖ | ➖ | ➖ | ➖ | ➖ | ➖ | ➖ | ✅ | ✅ |
| `novita` | openai | ✅ | ✅ | ✅ | ➖ | ➖ | ➖ | ➖ | ➖ | ✅ | ✅ | ✅ |
| `nvidia-nim` | openai | ✅ | ✅ | ✅ | ➖ | ✅ | ➖ | ➖ | ➖ | ➖ | ✅ | ✅ |
| `ollama` | openai | ✅ | ✅ | ✅ | ➖ | ➖ | ➖ | ➖ | ➖ | ➖ | ✅ | ✅ |
| `ollama-cloud` | openai | ✅ | ✅ | ✅ | ➖ | ➖ | ➖ | ➖ | ➖ | ➖ | ✅ | ➖ |
| `openai` | openai | ✅ | ✅ | ✅ | ✅ | ➖ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| `openrouter` | openai | ✅ | ✅ | ✅ | ➖ | ➖ | ➖ | ➖ | ➖ | ➖ | ✅ | ✅ |
| `perplexity` | openai | ✅ | ✅ | ➖ | ➖ | ➖ | ➖ | ➖ | ➖ | ➖ | ➖ | ✅ |
| `qwen` | openai | ✅ | ✅ | ✅ | ➖ | ➖ | ➖ | ➖ | ➖ | ✅ | ✅ | ✅ |
| `replicate` | native | ✅ | ✅ | ➖ | ✅ | ➖ | ➖ | ➖ | ➖ | ➖ | ➖ | ⛔ |
| `sambanova` | openai | ✅ | ✅ | ✅ | ➖ | ➖ | ➖ | ✅ | ➖ | ➖ | ✅ | ✅ |
| `together` | openai | ✅ | ✅ | ✅ | ✅ | ✅ | ➖ | ✅ | ✅ | ➖ | ✅ | ✅ |
| `vertex-ai` | native | ✅ | ✅ | ✅ | ✅ | ➖ | ➖ | ➖ | ➖ | ➖ | ➖ | ⛔ |
| `xai` | openai | ✅ | ✅ | ➖ | ✅ | ➖ | ➖ | ➖ | ➖ | ➖ | ✅ | ✅ |

### Totals

| Surface | Providers |
|---|:--:|
| Chat / Stream | 30 |
| Embeddings | 21 |
| Discovery (live `/models`) | 19 |
| Images | 10 |
| Transcription (STT) | 8 |
| Speech (TTS) | 6 |
| Rerank | 5 |
| Batch | 5 |
| Moderation | 2 |
| Proxy pass-through | 21 ✅ (plus 8 ⛔ native-wire that return 501) |

## Endpoints these surfaces serve

The gateway exposes the OpenAI-compatible HTTP API; each column above maps to an
endpoint. Rerank uses the Cohere-v2 contract, everything else the OpenAI contract.

| Endpoint | Column | Notes |
|---|---|---|
| `POST /v1/chat/completions` | Chat / Stream | `stream: true` for SSE |
| `POST /v1/completions` | Chat | Legacy text completions, served as a single-message chat |
| `POST /v1/embeddings` | Embeddings | |
| `POST /v1/images/generations` | Images | |
| `POST /v1/rerank` | Rerank | Cohere-v2 contract |
| `POST /v1/moderations` | Moderation | |
| `POST /v1/audio/transcriptions`, `/v1/audio/translations` | Transcribe | multipart upload |
| `POST /v1/audio/speech` | Speech | JSON in, binary audio out |
| `GET /v1/models` | Discovery | union of catalog, live discovery, and declared models |
| `GET /v1/capabilities` | — | per-provider parameter support |
| `/v1/files*`, `/v1/batches*` | Batch | pass-through to one configured `batch_target` |
| `/v1/responses`, `/v1/responses/*` | — | governed, **priced** pass-through (create is model-routed; id sub-routes pin to `responses_target`) |
| `/v1/*` (any other) | Proxy | transparent pass-through — ✅ providers only, ⛔ returns 501 |

## Notes

- **A provider is registered only when its credential is present.** Set at least
  one; see [`../.env.example`](../.env.example) for every provider's credential
  and its optional `<PROVIDER>_BASE_URL` override. Adding a provider needs no
  `main.go` change — see [Adding a New Provider](../AGENTS.md#adding-a-new-provider).
- **Discovery** ✅ means the provider enumerates its models live from a `/models`
  endpoint. A ➖ here is not a routing limitation: those providers still route
  every model the catalog, live discovery of *other* providers, or
  `targets[].models` names — several serve *any* model id the upstream accepts.
- **Proxy ⛔** is deliberate, not a defect. Eight providers return 501 on the raw
  `/v1/*` pass-through: the six native-wire ones (Anthropic, Bedrock, Cohere,
  Gemini, Vertex AI, Replicate) whose upstream is not OpenAI-shaped, plus the two
  Azure surfaces (`azure-openai`, `azure-foundry`) which are OpenAI-wire but route
  by deployment path and `api-version`, so a raw body cannot be forwarded
  verbatim. The translated surfaces above cover all of them. `ollama-cloud` is ➖
  (not proxiable) rather than ⛔ because its Provider deliberately does not
  implement the proxiable interface (a stability test asserts it must not).
## Not yet implemented (deferred by design)

Vendor endpoints with no OpenAI-shaped seam yet — deferred, not defects (every
model still routes through the surfaces above). Listed so the boundary is explicit:

- **Native-shape image generation** — `fireworks`, `novita`, `nvidia-nim`, `qwen`,
  `openrouter` expose text-to-image only on a non-OpenAI path (a native adapter each).
- **Live `/models` discovery** on `bedrock`, `cloudflare`, `cohere`, `gemini`,
  `replicate`, `vertex-ai` — each lists models on a non-OpenAI shape; no functional
  loss today (the catalog and `targets[].models` cover routing).
- **Native batch APIs** — `mistral`, `together`, `anthropic`, `gemini`, `vertex-ai`,
  `bedrock` run batch on bespoke job APIs, outside the OpenAI `/v1/batches` contract.
- **`replicate` embeddings** — runnable via the predictions API; no OpenAI endpoint on either side.
- **`ollama-cloud` proxy** — intentionally not proxiable (enforced by a stability test).

## Layout

```
providers/
├── <id>/                # one subpackage per provider (30) — <id>.go + surface files + tests
├── core/                # shared interfaces (contracts.go) + types + OpenAI/native translators
│   ├── openaicompat/    #   OpenAI-compatible chat/stream/embed/rerank/moderation/audio helpers
│   ├── anthropicwire/   #   Anthropic wire helper (shared by anthropic + bedrock)
│   └── imagenwire/       #   Google Imagen :predict helper
├── capabilities/        # provider × parameter support matrix (GET /v1/capabilities)
├── factory.go           # ProviderConfig, ProviderEntry, Capability* / CfgKey* constants
├── providers_list.go    # every built-in ProviderEntry (the registry)
├── names.go             # canonical NameXxx constants
└── registry.go          # runtime lookup by name
```
