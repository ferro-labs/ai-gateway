// Package apierror provides OpenAI-compatible JSON error response helpers.
package apierror

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"

	"github.com/ferro-labs/ai-gateway/internal/redact"
	"github.com/ferro-labs/ai-gateway/pkg/circuitbreaker"
	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

const codeModelNotFound = "model_not_found"

// OpenAI error type values. These are part of the response contract: clients
// switch on them, so they are named once here rather than repeated per branch.
const (
	errTypeServer         = "server_error"
	errTypeInvalidRequest = "invalid_request_error"
	errTypeRateLimit      = "rate_limit_error"
	errTypeUpstream       = "upstream_error"
	// errTypeInsufficientQuota is OpenAI's own value for an exhausted quota,
	// and it carries the same string in the type and the code fields there, so
	// it does here too.
	errTypeInsufficientQuota = "insufficient_quota"
)

// budgetPluginName is the registered name of the built-in spend-cap plugin,
// whose before_request denial is classified below.
//
// The NAME, not the PluginType — which is the discriminator the wrong status
// came from. PluginType is the enforcement-role axis: the plugin manager reads
// it to decide what fails open (errorFailsOpen), whether a transform may
// rewrite the routed model, and whether a guardrail reads request content.
// budget reports TypeRateLimit purely to reach the 429 branch below, and using
// a policy axis as a status selector is what produced this bug — a new
// PluginType existing only to select a status would repeat it one name along.
// Moving budget to TypeGuardrail is worse: it declares no ContentAgnostic, so
// it would enrol in Manager.HasBeforeRequestGuardrail and start refusing
// uninspectable /v1/* pass-through bodies in deployments running no content
// guardrail at all.
//
// So the type stays where it is and the classification keys on the one plugin
// whose denial this actually is. apierror_budget_test.go holds this string
// equal to the plugin's own Name(), so a rename cannot silently restore the 429.
const budgetPluginName = "budget"

// retryAfterSeconds is the Retry-After hint sent with a 429 the GATEWAY decided
// — a saturated target, a rate-limit plugin's denial — as opposed to one an
// upstream handed back with a wait of its own.
//
// The header is what makes the 429 work. openai-go retries 429 by default
// (internal/requestconfig.shouldRetry: 408, 409, 429, >=500) and reads
// Retry-After-Ms then Retry-After to decide how long to wait; with no hint it
// falls back to its own half-second-and-doubling schedule, so the rejections
// meant to relieve pressure are what produce the next burst. The Anthropic SDKs
// behave the same way.
//
// It is deliberately a constant rather than a figure derived from the limiter's
// own queue. The limiter knows its in-flight count and queue depth, but
// Retry-After is a TIME, and converting a queue depth into one needs a drain
// rate the limiter never measures — it would have to assume a per-request
// service time, which for an LLM call ranges from under a second to minutes and
// varies by model and prompt. A number computed from that assumption is
// confidently wrong rather than approximately right, and openai-go discards any
// hint of a minute or more outright (requestconfig.retryDelay), so an
// overestimate silently degrades to the SDK's own backoff having bought
// nothing. One second is the honest floor: it is short enough to always be
// worth honouring and it never claims to know when capacity returns. Measuring
// the drain rate is the upgrade path if a precise hint is ever worth its cost.
const retryAfterSeconds = "1"

// Messages returned to the caller. Each is chosen by CLASSIFICATION, never
// copied out of the error chain — see classifyRouteError.
const (
	msgModelNotFound  = "no configured provider serves the requested model"
	msgUpstreamModel  = "the provider that serves this model no longer offers it"
	msgSaturated      = "the gateway is at capacity for this request; retry shortly"
	msgInternal       = "the gateway could not complete the request"
	msgUpstreamAuth   = "the gateway could not authenticate with the upstream provider"
	msgUpstreamFailed = "the upstream provider returned no usable response"
	msgUpstreamTime   = "the upstream provider did not respond in time"
	msgRateLimited    = "rate limit exceeded"
	msgCircuitOpen    = "the upstream provider is temporarily unavailable; retry shortly"
)

// WriteOpenAI writes a unified OpenAI-compatible JSON error response.
//
// message is redacted before it is written. Upstream providers control their own
// error bodies, and those bodies can quote the credential the gateway presented,
// so the text reaching the client is filtered rather than trusted. Only the
// message value changes — status, type, and code are written as given.
func WriteOpenAI(w http.ResponseWriter, status int, message, errType, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{
			"message": redact.String(message),
			"type":    errType,
			"code":    code,
		},
	})
}

// upstreamStatusDetails maps the status a provider returned to the gateway onto
// the status the gateway returns to its caller. The two are not the same claim:
// the upstream status describes the request the *gateway* made, so it is
// re-attributed to whoever can actually act on it rather than forwarded verbatim.
//
//   - 429 stays 429. It is the one status where the upstream's instruction and the
//     caller's correct behaviour coincide: everyone slows down.
//   - 401/403 become 502. The credential the provider rejected is the operator's,
//     not the caller's. A 401 would tell the caller to fix a key they do not hold,
//     and OpenAI SDKs surface it as an authentication failure that discards the
//     session. What is broken is the gateway's upstream leg, so it is reported as
//     an upstream fault.
//   - 400/422 pass through. The provider read the translated request and rejected
//     its shape, which traces back to the caller's request and is theirs to fix.
//   - 404 is the model: the provider does not serve what was asked for, which the
//     caller resolves by asking for something else.
//   - Timeouts become 504 and everything else becomes 502 — the gateway reached a
//     provider that gave no usable answer, which is nothing the caller can fix.
func upstreamStatusDetails(statusErr *core.HTTPStatusError) routeErrorClass {
	switch statusErr.StatusCode {
	case http.StatusTooManyRequests:
		return routeErrorClass{http.StatusTooManyRequests, errTypeRateLimit, "rate_limit_exceeded", msgRateLimited}
	case http.StatusUnauthorized, http.StatusForbidden:
		return routeErrorClass{http.StatusBadGateway, errTypeUpstream, "upstream_auth_error", msgUpstreamAuth}
	case http.StatusNotFound:
		// Distinct from the routing 404 below. Routing found nobody; this found
		// the owner, called it, and IT said no — the catalog advertises a model
		// the provider has since retired. "No configured provider serves the
		// requested model" sends the operator to a targets list that is correct,
		// so the two are worded apart. The status and code stay as they are:
		// clients switch on the code, and both really are "that model is not
		// available here".
		return routeErrorClass{http.StatusNotFound, errTypeInvalidRequest, codeModelNotFound, msgUpstreamModel}
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		// The only upstream text that reaches the caller. The provider read the
		// translated request and rejected its shape, so its account is about the
		// caller's own request and is the one thing here they can act on — a
		// "max_tokens is too large" replaced by a generic sentence is a support
		// ticket. statusErr.Message is the provider's own text, taken from the
		// innermost error so none of the gateway's routing narrative ("all
		// providers failed: provider X attempt 2: …") rides out with it, and
		// WriteOpenAI still redacts it because the body is the provider's to
		// write and can quote the credential we presented.
		return routeErrorClass{statusErr.StatusCode, errTypeInvalidRequest, "invalid_request", statusErr.Message}
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return routeErrorClass{http.StatusGatewayTimeout, errTypeUpstream, "upstream_timeout", msgUpstreamTime}
	default:
		return routeErrorClass{http.StatusBadGateway, errTypeUpstream, "upstream_error", msgUpstreamFailed}
	}
}

// routeErrorClass is everything the caller is told about a failure.
//
// message belongs here, alongside the status and code, because it is a property
// of the CLASSIFICATION and not of the error chain. Writing err.Error() instead
// — which is what this did — hands out whatever text the chain accumulated on
// its way up, and that text is the gateway's own topology: "all providers
// failed: provider not found: anthropic" tells an API caller which providers
// are configured and which of them are broken. The caller cannot act on any of
// it, and the operator, who can, reads it in the log either way (see
// Gateway.routeError and recordSurfaceError, both of which log the full chain
// through internal/redact). OWASP's error-handling guidance is the general form
// of this: generic message to the user, detail to the log.
//
// The two exceptions are marked where they are made — a plugin's rejection
// reason and an upstream's account of a malformed request are written to be
// read by the caller, and are the caller's only route to a fix.
type routeErrorClass struct {
	status  int
	errType string
	code    string
	message string
}

// classifyRouteError maps a routing or plugin error to the status, OpenAI
// type/code, and caller-facing message. Decisions the gateway made itself are
// classified first; an upstream provider status is read last.
func classifyRouteError(err error) routeErrorClass {
	// A fail-closed plugin that broke did not deny anything — it never got far
	// enough to have an opinion. That is a fault on our side of the wire, so it
	// is reported as one. Dressing it up as a rejection would tell the client to
	// fix a request that was fine, or — for a rate-limit plugin whose backend is
	// down — hand back a 429 that invites every SDK to retry into the outage.
	// Its message names the plugin and whatever it failed to reach, which is
	// deployment detail; the caller gets the generic form.
	var failure *plugin.FailureError
	if errors.As(err, &failure) {
		return routeErrorClass{http.StatusInternalServerError, errTypeServer, "plugin_error", msgInternal}
	}

	// A plugin that DENIED the request is the one place a gateway decision is
	// addressed to the caller: Reason is written for them, it is why the request
	// was refused, and without it the refusal is unactionable. It is kept.
	var rejection *plugin.RejectionError
	if errors.As(err, &rejection) {
		switch rejection.Stage {
		case plugin.StageBeforeRequest:
			// An exhausted spend cap is not a rate limit, and the difference is
			// what the client does next. 429 is retryable in every OpenAI SDK's
			// policy (openai-go: 408, 409, 429, >=500) and WriteRouteError adds
			// Retry-After: 1 to it, but a cumulative USD cap clears on cost
			// roll-off or an explicit reset and never on a timer — so the
			// caller spent its whole backoff schedule re-asking a question
			// whose answer could not change, roughly once a second, forever.
			// 402 is in no SDK's retry set and carries no Retry-After, so the
			// first refusal is the last request. It is tested before the
			// rate-limit branch because budget reports TypeRateLimit.
			if rejection.Plugin == budgetPluginName {
				return routeErrorClass{http.StatusPaymentRequired, errTypeInsufficientQuota, errTypeInsufficientQuota, rejection.Error()}
			}
			if rejection.PluginType == plugin.TypeRateLimit {
				return routeErrorClass{http.StatusTooManyRequests, errTypeRateLimit, "rate_limit_exceeded", rejection.Error()}
			}
			return routeErrorClass{http.StatusBadRequest, errTypeInvalidRequest, "request_rejected", rejection.Error()}
		case plugin.StageAfterRequest:
			return routeErrorClass{http.StatusBadGateway, errTypeUpstream, "response_rejected", rejection.Error()}
		default:
			return routeErrorClass{http.StatusInternalServerError, errTypeServer, "request_rejected", rejection.Error()}
		}
	}

	// No configured target can serve this model: none is registered, or none
	// that is registered supports it. 404 rather than 500 because it is the
	// caller's to fix and retrying cannot change it — openai-go retries
	// 408/409/429/5xx and never 404, so this is the status that stops a config
	// mistake from becoming a retry storm.
	if errors.Is(err, core.ErrNoCapableProvider) {
		return routeErrorClass{http.StatusNotFound, errTypeInvalidRequest, codeModelNotFound, msgModelNotFound}
	}

	// The target is at its concurrency limit and its queue is full. This is
	// backpressure, not a failure: 429 tells the caller to back off and retry,
	// which is exactly the desired behaviour under saturation.
	if errors.Is(err, core.ErrProviderSaturated) {
		return routeErrorClass{http.StatusTooManyRequests, errTypeRateLimit, "provider_saturated", msgSaturated}
	}

	// Names only parameter names and the provider, never prompt content or
	// secrets, and exists to tell the caller which parameter to drop.
	var unsupportedParam *core.UnsupportedParamError
	if errors.As(err, &unsupportedParam) {
		return routeErrorClass{http.StatusBadRequest, errTypeInvalidRequest, "unsupported_parameter", unsupportedParam.Error()}
	}

	// The target's circuit is open, so the request was refused without any
	// upstream call. 503 is the status that says "come back later": every
	// OpenAI-compatible SDK backs off on it, whereas the 500 this used to
	// return reads as a bug in the gateway and sends an operator looking for
	// one. The breaker is checked before the upstream status below because an
	// open circuit is the gateway's own decision about a target, and a decision
	// is a more specific account than whatever failure originally opened it.
	if errors.Is(err, circuitbreaker.ErrCircuitOpen) {
		return routeErrorClass{http.StatusServiceUnavailable, errTypeUpstream, "upstream_unavailable", msgCircuitOpen}
	}

	// The gateway's own request_timeout fired, or the caller's deadline did:
	// either way nothing answered in time. 504 was already the answer for an
	// upstream that timed out; there was simply no branch for the deadline the
	// gateway imposes itself, so the one case an operator can fix by raising a
	// config value was reported as an internal error.
	//
	// It is classified after saturation on purpose: a request shed from a full
	// queue carries ctx.Err() alongside ErrProviderSaturated, and that is a 429,
	// not a timeout.
	if errors.Is(err, context.DeadlineExceeded) {
		return routeErrorClass{http.StatusGatewayTimeout, errTypeUpstream, "gateway_timeout", msgUpstreamTime}
	}

	// The upstream status is consulted last. One chain can carry both a provider
	// failure and the decision this gateway took about it — a target shed under
	// its own concurrency limit, a plugin that denied the call — and the
	// gateway's decision is the more specific account of what happened.
	var statusErr *core.HTTPStatusError
	if errors.As(err, &statusErr) {
		return upstreamStatusDetails(statusErr)
	}

	return routeErrorClass{http.StatusInternalServerError, errTypeServer, "routing_error", msgInternal}
}

// WriteModelNotFound reports that no configured target serves the model the
// caller named.
//
// It is byte for byte the answer classifyRouteError gives for
// core.ErrNoCapableProvider, exported so the one surface that decides this
// before the router is reached — the /v1/* pass-through, which resolves an
// owner from the request body — cannot drift from the four that decide it
// inside. The condition is identical wherever it is detected, so the status,
// code, and message are too: a client that switches on any of them sees one
// gateway rather than five.
func WriteModelNotFound(w http.ResponseWriter) {
	WriteOpenAI(w, http.StatusNotFound, msgModelNotFound, errTypeInvalidRequest, codeModelNotFound)
}

// RouteErrorDetails maps a routing or plugin error to an HTTP status and OpenAI
// error type/code.
func RouteErrorDetails(err error) (status int, errType, code string) {
	c := classifyRouteError(err)
	return c.status, c.errType, c.code
}

// WriteRouteError reports a routing or plugin failure to the client: it
// classifies err, sets a Retry-After header where one can be justified, and
// writes the OpenAI error envelope.
//
// Retry-After is set in two cases, and the first is not limited to 429. Any
// upstream response that carried a Retry-After hint propagates it, whatever
// status it is reported as — a 502 upstream_error built from a 503 that named
// its own wait keeps that wait. The hint is the upstream's statement about
// itself and is worth more than the status mapping laid over it. Second, a 429
// the gateway decided on its own (its rate limiter, a saturated target) carries
// no upstream hint, so it gets the constant rather than nothing: see
// retryAfterSeconds for why a constant and not a computed figure. No header is
// set for any other gateway-decided status.
//
// It exists so the four routed surfaces cannot drift on how a failure is
// reported. A throttled upstream in particular tells us how long to wait, and
// that hint is only useful if it reaches the caller — without it a client falls
// back to guessing, and every client guesses differently.
func WriteRouteError(w http.ResponseWriter, err error) {
	c := classifyRouteError(err)
	switch upstreamWait := core.RetryAfterFrom(err); {
	case upstreamWait > 0:
		// The upstream stated its own wait. It knows when it will be ready and
		// the gateway's constant must never overwrite that.
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(upstreamWait.Seconds()))))
	case c.status == http.StatusTooManyRequests:
		w.Header().Set("Retry-After", retryAfterSeconds)
	}
	WriteOpenAI(w, c.status, c.message, c.errType, c.code)
}
