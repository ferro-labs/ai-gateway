# Ferro Labs AI Gateway Roadmap

This roadmap tracks the path from `v1.0.0-rc.1` to `v1.0.0` and the priorities
immediately after the stable release.

## v1.0.0-rc.1

Status: In progress

### What ships in the release candidate

- 29 built-in providers behind a single OpenAI-compatible gateway surface
- 8 routing strategies for reliability, cost, latency, and experimentation
- 6 built-in OSS plugins for guardrails, caching, logging, rate limiting, and
  budget controls
- Admin APIs, dashboard UI, health checks, metrics, and request logging
- MCP integration for external tool servers and agentic loops

### What remains before stable

- Final release-candidate validation
- Final docs polish across quick start, providers, routing, and plugins
- Release packaging, tagging, and announcement work

## v1.0.0

Status: Planned

### Goals

- ship a stable OSS gateway with a clear and durable feature boundary
- make onboarding fast for new adopters
- tighten documentation and deployment guidance for production usage

### Priorities

- Documentation:
  quick start, provider reference, routing cookbook, plugin reference, MCP
  guide, migration guides
- Deployment:
  release validation, container polish, Kubernetes and Helm guidance
- Examples:
  expand the dedicated `ferro-labs/ai-gateway-examples` repo with migration,
  streaming, MCP, and routing examples
- Quality:
  stronger coverage, race-test confidence, benchmark publication, API docs

## After v1.0.0

- continue expanding provider coverage
- ship SDKs and stronger example coverage
- improve benchmark reporting and production guidance
- deepen ecosystem support around deployment and integrations
