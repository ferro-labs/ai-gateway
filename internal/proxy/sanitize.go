package proxy

import (
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/ferro-labs/ai-gateway/internal/redact"
)

// maxRedactableBody bounds how much of an upstream body is buffered in order to
// scan it. Only a body the gateway already declines to treat as the caller's
// payload reaches here, and every provider's is a short JSON or HTML document. A
// body larger than this is not something the gateway can vouch for, so it is
// replaced rather than relayed — the bound fails closed, it does not fall
// through.
const maxRedactableBody = 256 << 10

// genericUpstreamErrorBody replaces a body the gateway cannot scan.
const genericUpstreamErrorBody = `{"error":{"message":"upstream request failed","type":"server_error","code":"upstream_error"}}`

// injectedSecretToken replaces a credential this gateway itself put on the
// outbound request. It is distinct from redact.Values' [REDACTED:VAR] token so
// an operator reading a proxied response can tell the two apart: this one means
// "the upstream quoted our own credential back at us".
//
//nolint:gosec // G101: this is the replacement token written over a credential, not a credential.
const injectedSecretToken = "[REDACTED:GATEWAY_CREDENTIAL]"

// sanitizeResponse redacts credentials from an upstream response before the
// pass-through relays it to the client.
//
// The pass-through injects the gateway's provider credential into every
// outbound request (Handler's Rewrite replaces the client's Authorization with
// the provider's). An upstream that quotes the offending key back has that quote
// relayed verbatim, handing the gateway's own credential to whoever made the
// request — including an anonymous one when ALLOW_UNAUTHENTICATED_PROXY is set.
// The client never sent that credential and must never receive it.
//
// This is the same harm the translated surfaces avoid by only ever retaining a
// parsed message string, redacted by internal/redact. The pass-through has no
// parsed message to redact: it copies an io.Reader onto a header map it did not
// build. So the scan happens here, at the one point every proxied response
// passes through.
//
// Headers are scanned on every response and bodies on every non-2xx; see
// sanitizeHeader and bodyIsRedactable for why the two differ.
func sanitizeResponse(resp *http.Response, injected []string) error {
	sanitizeHeader(resp.Header, injected)
	dropTrailers(resp)
	return sanitizeBody(resp, injected)
}

// dropTrailers withholds a response's trailers rather than relaying them
// unscanned.
//
// A trailer's value does not exist yet at the only point this code runs. The
// response is inspected before its body is read, and a trailer arrives after
// it: resp.Trailer holds the announced key with an empty value here, and is
// filled in later, once the reverse proxy has already begun copying to the
// client. Scanning the map at this point therefore scans nothing — the obvious
// one-line fix reads every value as "".
//
// What is left is a choice between relaying a value the gateway cannot see and
// not relaying it. On this path the credential the proxy itself injected is
// exactly what an upstream might quote back, so an unscannable slot is refused,
// the same way a compressed body past the size bound is. The cost is bounded:
// the endpoints this pass-through exists for carry no trailer, and the
// announcement goes with the values so a client is not told to expect one that
// never comes.
//
// Clearing the field here is necessary and NOT sufficient, which is the whole
// reason this is more than two lines. The transport merges the wire trailers
// back into resp.Trailer when the body reaches EOF or is closed — both of which
// happen after ModifyResponse has returned — and httputil.ReverseProxy then
// reads the field again: it derives the downstream announcement from the
// length it holds before the copy, and relays whatever it holds immediately
// after res.Body.Close(). Nothing downstream reads resp.Header's own Trailer
// line, which removeHopByHopHeaders has already removed by the time this runs.
//
// So the field is cleared at both points the proxy reads it: here, so nothing
// is announced, and again when the body is closed, which is the last moment
// before the values would be relayed.
func dropTrailers(resp *http.Response) {
	resp.Trailer = nil
	resp.Header.Del("Trailer")
	// A status that carries no body carries no trailer section either — the
	// transport never parses chunks for one, so nothing can be merged back and
	// there is nothing to strip. Skipping them is also what keeps a 101 intact:
	// its resp.Body is the tunnelled connection, which handleUpgradeResponse
	// requires to stay an io.ReadWriteCloser, so wrapping it would break every
	// upgrade endpoint.
	bodyless := resp.StatusCode < 200 ||
		resp.StatusCode == http.StatusNoContent ||
		resp.StatusCode == http.StatusNotModified
	if resp.Body == nil || bodyless {
		return
	}
	resp.Body = trailerStripper{ReadCloser: resp.Body, resp: resp}
}

// trailerStripper re-clears resp.Trailer on Close, undoing the transport's
// merge of the wire trailers into the field dropTrailers emptied.
type trailerStripper struct {
	io.ReadCloser
	resp *http.Response
}

func (t trailerStripper) Close() error {
	err := t.ReadCloser.Close()
	t.resp.Trailer = nil
	return err
}

// sanitizeHeader scrubs every response header value, whatever the status.
//
// Header scanning is not gated on status the way body buffering is, because it
// shares none of the cost that gating pays for: the values are already in memory
// and already fully read, so there is no body to buffer, no stream to break and
// nothing to bound. A 401's WWW-Authenticate is the standards-defined slot for
// explaining a rejected credential (RFC 9110 §11.6.1) and so the likeliest place
// for a provider to quote one; a 3xx Location can carry a key in its query
// string; and a 101's headers reach the client too.
//
// Every header is scanned rather than a chosen few, because scrubbing matches
// credential values *exactly* — the literal strings this request carried
// upstream, plus the literal values redact has enrolled. Nothing is matched on
// shape, so a header that merely looks credential-shaped (an ETag, a Set-Cookie,
// an opaque request id) cannot be rewritten by accident: the only way a header
// changes is if it genuinely contains a credential the gateway holds, and that
// is a value no upstream should be sending to a client.
func sanitizeHeader(header http.Header, injected []string) {
	for _, values := range header {
		for i, value := range values {
			values[i] = scrub(value, injected)
		}
	}
}

// bodyIsPlainText reports whether a body can be read as the text this code
// scans, judged over every Content-Encoding field line rather than the first.
//
// http.Header.Get returns only the first line, so an upstream sending
//
//	Content-Encoding: identity
//	Content-Encoding: gzip
//
// answered "identity" and had its compressed bytes scanned as text. Nothing
// matched, and the credential inside them was relayed. The guard is documented
// as failing closed; read that way it fell through, and the client received
// bytes that gunzip back to the key.
//
// Encodings are a list applied in order, so the only body this code can read is
// one with no encoding at all, or the single no-op. Anything else — several
// lines, several values on one line, or one name that is not identity — is
// refused rather than guessed at.
func bodyIsPlainText(header http.Header) bool {
	for _, line := range header.Values("Content-Encoding") {
		for _, name := range strings.Split(line, ",") {
			if name = strings.TrimSpace(name); name != "" && !strings.EqualFold(name, "identity") {
				return false
			}
		}
	}
	return true
}

// sanitizeBody buffers, scans and rewrites a response body when the status says
// the body is the upstream's diagnostic rather than the caller's payload.
func sanitizeBody(resp *http.Response, injected []string) error {
	if resp.Body == nil || !bodyIsRedactable(resp.StatusCode) {
		return nil
	}

	// A compressed body cannot be scanned as text. Decompressing to search it
	// would mean honouring an attacker-influenced encoding on an error path;
	// replacing it costs only a diagnostic the gateway cannot vouch for.
	if !bodyIsPlainText(resp.Header) {
		return replaceBody(resp, genericUpstreamErrorBody)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRedactableBody+1))
	_ = resp.Body.Close()
	if err != nil || len(body) > maxRedactableBody {
		return replaceBody(resp, genericUpstreamErrorBody)
	}
	return replaceBody(resp, scrub(string(body), injected))
}

// bodyIsRedactable reports whether a response body may be buffered and scanned.
// Three kinds of status are excluded, for three different reasons.
//
// A 1xx has no body to scan. The one such response the pass-through sees is 101
// Switching Protocols, where resp.Body is not a body at all but the tunnelled
// connection: httputil.ReverseProxy hands it to handleUpgradeResponse as an
// io.ReadWriteCloser and copies it raw in both directions, so buffering or
// replacing it breaks every upgrade endpoint (/v1/realtime). The 101's *headers*
// are still scanned — handleUpgradeResponse writes them from the map
// sanitizeHeader just went through — but the frames that follow are unscannable,
// and deliberately so.
//
// A 2xx is the caller's own payload — possibly streaming, possibly binary — and
// buffering it would both corrupt streaming pass-through (/v1/realtime,
// /v1/audio/*) and cost memory on every request. This is the one accepted gap: a
// 200 that echoes a credential in its body is relayed. No provider does, and the
// credential-echo risk lives in the paths that report a rejected credential,
// which is where the cost is affordable. 204 falls here and carries no body.
//
// A 304 carries no body by definition, and inventing framing headers for one
// would misreport the cached response it stands in for.
//
// Everything else — every 3xx redirect, every 4xx and every 5xx — is scanned. A
// 3xx is not a success payload the caller owns: the reverse proxy does not
// follow it, so both its Location and its body reach the client intact.
func bodyIsRedactable(status int) bool {
	switch {
	case status < 200:
		return false
	case status < 300:
		return false
	case status == http.StatusNotModified:
		return false
	default:
		return true
	}
}

// scrub replaces every credential the gateway can recognise in s.
//
// Two sources are applied, and both are needed:
//
//   - injected — the exact values Handler put on this request. Precise, and it
//     covers a credential supplied through a direct ProviderConfig map (cloud
//     or tenant credential injection), which never appears in the environment.
//   - redact.Values — every credential-shaped environment value. Catches a
//     *different* credential than the one we injected: an upstream that echoes
//     a co-tenant's key, or a second provider's key quoted in a federated error.
//
// Both match literal values, so a string carrying no credential comes back
// unchanged and neither source needs a redactor of its own here.
func scrub(s string, injected []string) string {
	for _, secret := range injected {
		s = strings.ReplaceAll(s, secret, injectedSecretToken)
	}
	return redact.Values(s)
}

// replaceBody swaps in a body and restates the framing headers to match it.
//
// Content-Length must be corrected because redaction changes the length, and a
// stale one truncates the response or hangs the client. Content-Encoding is
// dropped because whatever replaces the body is plain text.
func replaceBody(resp *http.Response, body string) error {
	resp.Body = io.NopCloser(strings.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
	resp.Header.Del("Content-Encoding")
	resp.TransferEncoding = nil
	return nil
}

// injectedSecrets extracts the credential values a set of auth headers carries,
// so a response echoing any of them can be scanned for it.
//
// Only headers whose *name* marks them as carrying a credential contribute.
// AuthHeaders is a provider's whole outbound header set, and a provider is free
// to put a non-credential in it — anthropic sends an API version there. Enrolling
// such a value would blank it out wherever it appeared in a relayed header or
// body, which corrupts ordinary traffic rather than redacting anything.
// redact.IsCredentialName is the same whole-component judgement already applied
// to environment variables and plugin config keys, and it recognises every
// credential header the providers send: Authorization, x-api-key, api-key,
// x-goog-api-key.
//
// Both the whole header value and the token after a scheme prefix are returned:
// an upstream may quote "Bearer sk-…" as it received it, or just "sk-…".
// Values shorter than redact.MinSecretLength are skipped for the reason that
// constant documents — a short value redacts ordinary words out of the response.
func injectedSecrets(authHeaders map[string]string) []string {
	if len(authHeaders) == 0 {
		return nil
	}
	secrets := make([]string, 0, len(authHeaders)*2)
	add := func(v string) {
		if len(v) >= redact.MinSecretLength {
			secrets = append(secrets, v)
		}
	}
	for name, value := range authHeaders {
		if !redact.IsCredentialName(name) {
			continue
		}
		add(value)
		if _, token, found := strings.Cut(value, " "); found {
			add(token)
		}
	}
	return secrets
}
