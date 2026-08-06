# Security Policy

## Supported Versions

| Version | Supported |
|---------|-----------|
| 1.1.x   | ✅ Yes    |
| 1.0.x   | ❌ No     |

## Reporting a Vulnerability

**Do not open a public GitHub issue for security vulnerabilities.**

Please report security issues by emailing: **security@ferrolabs.ai**

Include the following in your report:

- A description of the vulnerability
- Steps to reproduce
- Potential impact
- Any suggested mitigations (optional)

You can expect an acknowledgement within **48 hours** and a full response within **7 days**.

Once a fix is ready, we will:

1. Coordinate a responsible disclosure timeline with you.
2. Publish a patched release.
3. Credit you in the release notes (unless you prefer anonymity).

## Security Considerations

Ferro Labs AI Gateway acts as a reverse proxy for LLM API calls. Operators should be aware of:

- **API key exposure**: Provider API keys are read from environment variables. Never commit keys to source control or expose them in logs.
- **Admin API**: The `/admin` routes are protected by bearer token. Admin tokens should be rotated regularly and never shared.
- **Plugin inputs**: Word-filter and other guardrail plugins operate on request content. They are not a substitute for proper input sanitization on the client side.
- **Network trust**: The gateway does not enforce TLS between itself and providers by default; ensure your deployment network is trusted or configure TLS at the load balancer.

### Pass-through credential redaction

A `/v1/*` request the gateway does not handle natively is forwarded by the pass-through proxy,
which replaces the caller's `Authorization` header with the provider credential the gateway
holds. An upstream that quotes a rejected credential back would otherwise hand that credential
to whoever made the request, so proxied responses are scanned on the way out and every
credential the gateway recognises is replaced before the response is relayed. The natively
handled surfaces do not need this: they retain a parsed error message, which is redacted
before it is stored or returned.

**What is scanned**

- **Every response header, at every status**, including the headers of a `101 Switching
  Protocols` upgrade. A 401's `WWW-Authenticate` is the standards-defined place to explain a
  rejected credential, and a 3xx `Location` can carry one in its query string.
- **Response bodies on 3xx, 4xx and 5xx** — the statuses where the body is the upstream's
  diagnostic rather than the caller's payload.

Within those bodies the scan fails closed. A compressed body, and a body larger than 256 KiB,
is replaced with a generic upstream-error document rather than relayed unread. Announced
trailers are dropped from every response, because a trailer's value does not exist yet at the
point the scan runs.

**What is not scanned**

These are accepted limits with reasons behind them, not defects:

- **2xx response bodies.** A success body is the caller's own payload, possibly streaming and
  possibly binary. Buffering it would break streaming and upgrade pass-through and would cost
  memory on every request, so a 2xx that echoed a credential in its body would be relayed. The
  credential-echo risk lives in the responses that report a *rejected* credential, which is
  where the scan runs.
- **Frames after a `101` upgrade.** The upgrade response's headers are scanned, but the
  tunnelled connection that follows is copied raw in both directions and cannot be inspected.
- **Encoded or split credentials.** Matching is literal, against the exact credential values
  the gateway holds — the one it injected on this request, plus credential-shaped values from
  its own environment. A base64-encoded, chunked, or otherwise transformed rendering of the
  same credential is not recognised. Literal matching is also what keeps an ETag, a cookie or
  an opaque request id from being rewritten by accident: a header changes only if it genuinely
  contains a credential the gateway holds.

Operators pointing the pass-through at an upstream they do not control should treat 2xx
payloads as relayed verbatim. Keep `ALLOW_UNAUTHENTICATED_PROXY` unset — it removes
authentication from every `/v1/*` endpoint, and startup refuses it outright under
`GATEWAY_ENV=production` — and rotate provider credentials on your normal schedule.
