# Security Policy

## Supported Versions

| Version | Supported |
|---------|-----------|
| 0.1.x   | ✅ Yes    |

## Reporting a Vulnerability

**Do not open a public GitHub issue for security vulnerabilities.**

Please report security issues by emailing: **shahmitul005@gmail.com**

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

Ferro AI Gateway acts as a reverse proxy for LLM API calls. Operators should be aware of:

- **API key exposure**: Provider API keys are read from environment variables. Never commit keys to source control or expose them in logs.
- **Admin API**: The `/admin` routes are protected by bearer token. Admin tokens should be rotated regularly and never shared.
- **Plugin inputs**: Word-filter and other guardrail plugins operate on request content. They are not a substitute for proper input sanitization on the client side.
- **Network trust**: The gateway does not enforce TLS between itself and providers by default; ensure your deployment network is trusted or configure TLS at the load balancer.
