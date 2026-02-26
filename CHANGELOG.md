# Changelog

All notable changes to FerroGateway will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.0] — 2026-02-26

### Added

- **10 LLM Providers**: OpenAI, Anthropic, Google Gemini, Mistral, Groq, Together AI, Azure OpenAI, Cohere, DeepSeek, Ollama (local)
- **4 Routing Strategies**: single provider, fallback with retries + exponential backoff, weighted load balancing, conditional (model-based) routing
- **Transparent Pass-Through Proxy**: Seamless proxying for non-chat endpoints (audio, images, files) with automatic auth injection
- **Streaming**: Server-Sent Events (SSE) support for all providers
- **Plugin System**: Extensible lifecycle hooks (before_request, after_request, on_error) with plugin registry
- **Built-in Plugins**:
  - `response-cache` — exact-match response caching (in-memory LRU with TTL)
  - `word-filter` — configurable word/phrase blocklist guardrail
  - `max-token` — enforce max token, message count, and input length limits
  - `request-logger` — structured JSON request/response logging
- **API Key Management**: In-memory key store with scoped RBAC (admin, read_only), key rotation, expiration
- **OpenAI-Compatible API**: `/v1/chat/completions`, `/v1/models`, `/health`
- **Admin API**: Key CRUD, provider listing, health checks under `/admin/`
- **Configuration**: JSON and YAML config files with validation, CLI validator
- **CLI Tool**: `ferrogw-cli validate`, `ferrogw-cli plugins`, `ferrogw-cli version`
- **Deployment**: Dockerfile, docker-compose.yml
- **License**: Apache License 2.0
