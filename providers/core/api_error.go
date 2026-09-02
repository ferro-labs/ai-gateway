package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// apiErrorEnvelope covers the OpenAI {"error":{"message":…}} error body shape,
// the FastAPI-style {"detail":"…"} envelope some providers (e.g. AI21) return for
// gateway-level errors, and the flat {"message":…} shape.
//
// It is deliberately an allow-list of KNOWN envelopes, not a general body
// reader: anything it cannot decode is discarded rather than surfaced. See
// upstreamMessage.
type apiErrorEnvelope struct {
	// Error is raw because it is not one shape. OpenAI, Anthropic and Gemini nest
	// the text in an object ({"error":{"message":…}}); xAI and Hugging Face send a
	// plain string ({"error":"…"}). Decoding it into a struct made the string form
	// fail the whole unmarshal, so the upstream's own words were dropped and the
	// caller got a bare status phrase. See errorMessage.
	Error json.RawMessage `json:"error"`
	// Detail is raw because the field is not one shape. FastAPI-backed providers
	// send a plain string for a gateway-level error and an ARRAY of validation
	// objects for a malformed request (Mistral does both); some (DeepInfra) send
	// an object carrying the reason under "error"/"message". Decoding it into a
	// string made the other kinds fail the whole unmarshal, so a 422 that named
	// the offending field was discarded and reported as bare "Unprocessable
	// Entity" — the one status whose upstream text reaches the caller.
	Detail  json.RawMessage `json:"detail"`
	Message string          `json:"message"`
}

// errorMessage renders an "error" field that is either a plain string — xAI and
// Hugging Face send {"error":"…"} — or an object nesting the text under
// "message" ({"error":{"message":…}}, the OpenAI/Anthropic/Gemini shape) or
// "error". Any other shape contributes nothing, keeping the allow-list rule.
func errorMessage(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return objectMessage(raw)
}

// objectMessage reads a single reason string from an object-shaped error field,
// preferring "message" and falling back to "error" — the two keys providers nest
// a plain-string reason under. It returns "" for any other shape, so an
// unrecognised body still contributes nothing.
func objectMessage(raw json.RawMessage) string {
	var obj struct {
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(raw, &obj) != nil {
		return ""
	}
	if obj.Message != "" {
		return obj.Message
	}
	return obj.Error
}

// detailMessage renders a "detail" field that is either a plain string or an
// array of FastAPI validation objects, joining the objects' own "msg" values.
//
// Only "msg" is read, never the element verbatim: the surrounding rule (see
// upstreamMessage) is that an unrecognised body contributes nothing, and an
// array element also carries "input", which echoes back the request value that
// failed validation.
func detailMessage(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var items []struct {
		Msg string `json:"msg"`
	}
	if json.Unmarshal(raw, &items) == nil {
		msgs := make([]string, 0, len(items))
		for _, it := range items {
			if it.Msg != "" {
				msgs = append(msgs, it.Msg)
			}
		}
		return strings.Join(msgs, "; ")
	}
	// An object-shaped detail, e.g. DeepInfra's {"error":"…"} on a 404 or a
	// {"message":"…"} reason. An unrecognised object still contributes nothing.
	return objectMessage(raw)
}

// MaxUpstreamMessageRunes bounds the upstream text an HTTPStatusError may
// carry. An upstream error body is written by someone else and is not
// length-checked by anyone: a stack trace, an HTML error page, or a
// multi-megabyte diagnostic all arrive through the same field, and that field
// reaches the caller on a 400/422 and every log line in between. Kubernetes'
// client-go bounds the same thing at 2048 bytes
// (rest/request.go maxUnstructuredResponseTextBytes); this follows it.
const MaxUpstreamMessageRunes = 2048

// HTTPStatusError is a provider error carrying the upstream HTTP status code
// as a typed field, so callers classify errors via errors.As instead of parsing
// the code back out of a formatted message.
//
// It is the gateway's whole provider→gateway error contract. Every provider
// returns one of these (or an error wrapping one) whenever an upstream answered
// with a non-success status, and nothing downstream — retry, the circuit
// breaker, HTTP classification — infers a status any other way.
//
// Message and Error() are two different audiences and the split is load-bearing:
//
//   - Message is the UPSTREAM's own account of the failure, bounded and taken
//     only from a recognised error envelope. It is the one piece of upstream
//     text that reaches the caller (on a 400/422, see internal/apierror), so it
//     names no provider and never carries an unrecognised response body.
//   - Error() is the OPERATOR's line. It prefixes the provider and status, so a
//     log or a span says which backend failed and how.
//
// Anything further — which call failed, which attempt, which target — belongs in
// a %w wrapper around this error, not inside Message. errors.As reaches through.
type HTTPStatusError struct {
	// StatusCode is the status the upstream returned. Never 0: an error with no
	// HTTP response is not one of these.
	StatusCode int
	// Provider is the backend that produced the failure. Operator-facing only —
	// it is never written into Message, so naming the caller's chosen model does
	// not also disclose which vendor served it.
	Provider string
	// Message is the upstream's bounded, structured account of the failure. It
	// is NOT the raw response body: see StatusError.
	Message string
	// RetryAfter carries the upstream Retry-After hint, or 0 when the response
	// did not supply a usable one. The fallback strategy honors it in preference
	// to its own computed backoff, so a 429/503 is retried when the provider says
	// it is ready rather than on a guess.
	RetryAfter time.Duration
	// Code is the provider's own error code from its envelope, when it sent
	// one: OpenAI's error.code ("context_length_exceeded"), Gemini's
	// error.status ("INVALID_ARGUMENT"). Preserved so a failure class can be
	// decided on what the provider said rather than on its status alone.
	Code string
	// Type is the provider's own error type: OpenAI's and Anthropic's
	// error.type ("invalid_request_error").
	Type string
}

// Error implements error. It is the operator-facing rendering; the caller-facing
// text is Message.
func (e *HTTPStatusError) Error() string {
	if e.Provider == "" {
		return fmt.Sprintf("API error (%d): %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("%s API error (%d): %s", e.Provider, e.StatusCode, e.Message)
}

// HTTPStatus reports the upstream status, satisfying the same accessor
// UnsupportedParamError offers.
func (e *HTTPStatusError) HTTPStatus() int { return e.StatusCode }

// StatusError builds the gateway's typed provider error from an
// already-extracted upstream message. It is the single constructor every other
// one funnels through, so the bounding rule below cannot be bypassed by
// building the struct by hand.
//
// message must be the upstream's own words, pulled out of an error shape that
// provider actually documents — never a raw response body. A body the provider
// did not write is not the provider talking: a WAF or a load balancer in front
// of an upstream answers with its own HTML, and that HTML has been observed
// carrying an internal hostname, an internal IP, an account id, a live
// credential and a filesystem path. Passing "" here is the correct answer for
// any body that did not parse; a generic status phrase stands in.
//
// The result is bounded to MaxUpstreamMessageRunes. CWE-209 and OWASP's
// error-handling guidance are the general form of both rules: a generic message
// to the caller, the detail to the operator's log.
func StatusError(provider string, status int, message string) *HTTPStatusError {
	message = strings.TrimSpace(message)
	if message == "" {
		message = http.StatusText(status)
	}
	if message == "" {
		message = "unexpected response"
	}
	return &HTTPStatusError{
		StatusCode: status,
		Provider:   provider,
		Message:    truncateRunes(message, MaxUpstreamMessageRunes),
	}
}

// maxRateLimitReset bounds the X-RateLimit-Reset fallback below. That header
// carries the seconds remaining in the current rate-limit window, for which a
// day is already generous; a larger value is an absolute Unix timestamp, which
// other APIs publish under the same name. Honouring one as a delay would put
// Retry-After decades out. Out-of-range means "no usable hint", exactly as it
// does in ParseRetryAfter.
const maxRateLimitReset = 24 * time.Hour

// WithRetryAfter applies the Retry-After hint carried by h and returns e, so a
// provider constructs and annotates in one expression.
//
// X-RateLimit-Reset is read when Retry-After is absent. Several providers
// (Mistral among them) throttle with only the rate-limit header set, and
// without it a 429 that stated its own wait was retried on the fallback
// strategy's guess instead. It is read for every provider rather than an
// allow-listed few: it is a numeric delta-seconds header with one meaning, and
// a per-provider list is a thing to keep up to date for no gain.
func (e *HTTPStatusError) WithRetryAfter(h http.Header) *HTTPStatusError {
	e.RetryAfter = ParseRetryAfter(h.Get("Retry-After"))
	if e.RetryAfter == 0 {
		if d := ParseRetryAfter(h.Get("X-RateLimit-Reset")); d > 0 && d <= maxRateLimitReset {
			e.RetryAfter = d
		}
	}
	return e
}

// truncateRunes bounds s to n runes, appending an ellipsis when it cuts. It
// counts runes rather than bytes so a cut never splits a multi-byte character
// into invalid UTF-8, which JSON encoding would then render as U+FFFD.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	count := 0
	for i := range s {
		if count == n {
			return s[:i] + "…"
		}
		count++
	}
	return s
}

// upstreamMessage extracts the provider's own error text from body, or returns
// "" when body is not one of the envelopes apiErrorEnvelope recognises.
//
// Returning "" for an unrecognised body is the point: the alternative — using
// the body itself — is what let an intermediary's HTML error page reach a
// caller verbatim.
func upstreamMessage(body []byte) string {
	var e apiErrorEnvelope
	if json.Unmarshal(body, &e) != nil {
		return ""
	}
	if m := errorMessage(e.Error); m != "" {
		return m
	}
	if d := detailMessage(e.Detail); d != "" {
		return d
	}
	return e.Message
}

// maxRetryAfterSeconds is the largest delta-seconds value a time.Duration can
// hold. Beyond it the multiply by time.Second wraps past MaxInt64 into a
// negative duration, which the fallback strategy would honour as "retry now".
const maxRetryAfterSeconds = int64(math.MaxInt64) / int64(time.Second)

// ParseRetryAfter parses an HTTP Retry-After header value (RFC 9110 §10.2.3),
// which is either delta-seconds ("120") or an HTTP-date. It returns 0 when the
// value is absent, unparseable, non-positive, out of range, or already in the
// past — all of which mean "no usable hint", never a negative wait.
func ParseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if secs, err := strconv.ParseInt(value, 10, 64); err == nil {
		if secs <= 0 || secs > maxRetryAfterSeconds {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(value); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// RetryAfterFrom returns the Retry-After hint carried by err, or 0 when err is
// not a provider status error or carried no usable hint.
func RetryAfterFrom(err error) time.Duration {
	var statusErr *HTTPStatusError
	if errors.As(err, &statusErr) {
		return statusErr.RetryAfter
	}
	return 0
}

// APIErrorFromResponse builds a provider error from a non-success HTTP response,
// capturing the Retry-After hint alongside the status code. Prefer it over
// APIError wherever the *http.Response is in hand, so throttling responses can
// drive retry backoff instead of being guessed at.
func APIErrorFromResponse(label string, resp *http.Response, body []byte) error {
	return StatusError(label, resp.StatusCode, upstreamMessage(body)).withEnvelope(body).WithRetryAfter(resp.Header)
}

// APIError builds a provider error from a non-success HTTP response body,
// reading the message out of a recognised error envelope. label is the
// provider's name (e.g. "groq"). A body in no recognised shape contributes
// nothing — see StatusError for why. Prefer APIErrorFromResponse when the
// *http.Response is in hand.
func APIError(label string, status int, body []byte) error {
	return StatusError(label, status, upstreamMessage(body)).withEnvelope(body)
}

// withEnvelope records the provider's own code and type from body, when the
// envelope carries them. Both are short identifiers the provider documents,
// never free text, so they are safe to keep whole.
func (e *HTTPStatusError) withEnvelope(body []byte) *HTTPStatusError {
	e.Code, e.Type = upstreamCodeAndType(body)
	return e
}

// upstreamCodeAndType reads the identifiers out of an object-shaped
// {"error":{…}} envelope: code and type as OpenAI and Anthropic spell them,
// with Gemini's status standing in for a code. A numeric code (Gemini repeats
// the HTTP status there) is rendered as its digits.
func upstreamCodeAndType(body []byte) (code, typ string) {
	var e apiErrorEnvelope
	if json.Unmarshal(body, &e) != nil || len(e.Error) == 0 {
		return "", ""
	}
	var fields struct {
		Code   json.RawMessage `json:"code"`
		Type   string          `json:"type"`
		Status string          `json:"status"`
	}
	if json.Unmarshal(e.Error, &fields) != nil {
		return "", ""
	}
	if len(fields.Code) > 0 {
		var asString string
		if json.Unmarshal(fields.Code, &asString) == nil {
			code = asString
		} else {
			// Numeric: Gemini repeats the HTTP status here, and its status
			// string ("INVALID_ARGUMENT") is the identifier worth keeping.
			var asNumber json.Number
			if json.Unmarshal(fields.Code, &asNumber) == nil {
				code = asNumber.String()
			}
		}
	}
	if fields.Status != "" && (code == "" || code == fields.Status || isDigits(code)) {
		code = fields.Status
	}
	return truncateRunes(code, 64), truncateRunes(fields.Type, 64)
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// IsContextLengthError reports whether err is a provider's own statement that
// the request exceeded the model's context window — the one deterministic
// 4xx that a different model can fix, and so the one a pool mode fails over
// on. It recognises the envelopes the OpenAI-compatible, Anthropic and Gemini
// families document; any other 4xx, including a 400 that merely resembles
// one, is not this class and stops routing as before.
func IsContextLengthError(err error) bool {
	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) {
		return false
	}
	if statusErr.StatusCode != http.StatusBadRequest && statusErr.StatusCode != http.StatusRequestEntityTooLarge {
		return false
	}
	message := strings.ToLower(statusErr.Message)
	switch {
	// OpenAI, Azure OpenAI and the compatible providers that copy the code.
	case statusErr.Code == "context_length_exceeded":
		return true
	// OpenAI-compatible providers that copy the message but send no code:
	// "This model's maximum context length is N tokens. However, …".
	case statusErr.Type == "invalid_request_error" && strings.Contains(message, "maximum context length"):
		return true
	// Anthropic: {"type":"error","error":{"type":"invalid_request_error",
	// "message":"prompt is too long: 213462 tokens > 200000 maximum"}}.
	case statusErr.Type == "invalid_request_error" && strings.Contains(message, "prompt is too long"):
		return true
	// Gemini: {"error":{"code":400,"status":"INVALID_ARGUMENT","message":"The
	// input token count (1500000) exceeds the maximum number of tokens allowed
	// (1048576)."}}.
	case statusErr.Code == "INVALID_ARGUMENT" && strings.Contains(message, "token count") && strings.Contains(message, "exceeds"):
		return true
	}
	return false
}

// UnsupportedParamError is returned by the reject compatibility mode when a
// request sets parameters the target provider cannot express. It is a distinct
// type (not a generic upstream HTTPStatusError) so the HTTP layer can map it to
// a 400 invalid_request_error without affecting how upstream provider errors are
// classified. It names only parameter names and the provider — never prompt
// content or secrets — so it is safe to return to the caller.
type UnsupportedParamError struct {
	// Provider is the target provider that cannot express the parameters.
	Provider string
	// Params are the offending OpenAI parameter names, in stable order.
	Params []string
}

// Error implements error.
func (e *UnsupportedParamError) Error() string {
	return fmt.Sprintf(
		"provider %q does not support request parameter(s): %s",
		e.Provider, strings.Join(e.Params, ", "),
	)
}

// HTTPStatus reports the HTTP status this error maps to (400 Bad Request).
func (e *UnsupportedParamError) HTTPStatus() int { return http.StatusBadRequest }

// NewUnsupportedParamError builds the reject-mode error naming the request
// parameters the provider cannot express.
func NewUnsupportedParamError(provider string, params []string) error {
	return &UnsupportedParamError{Provider: provider, Params: params}
}
