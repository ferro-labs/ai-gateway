package bedrock

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// bedrockResponseID synthesizes a response ID for the Bedrock model families
// whose InvokeModel response carries none (Nova, Titan, Llama), so gateway
// responses always expose an ID as the OpenAI contract expects.
func bedrockResponseID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "bedrock"
	}
	return "bedrock-" + hex.EncodeToString(b[:])
}

// bedrockInvokeError translates an InvokeModel/InvokeModelWithResponseStream
// failure into a *core.HTTPStatusError when the AWS SDK received an upstream
// HTTP response (e.g. a ThrottlingException or ValidationException), so the
// status survives as a typed field: without it every Bedrock error looks like
// status 0, which makes a genuine 429 trip the circuit breaker (rate limits
// must not) and makes a deterministic 4xx get retried against the same target
// instead of failing fast.
//
// It is the smithy half of the same adapter pattern openai-go gets in
// providers/openai.sdkError — only the package importing an SDK can name that
// SDK's error type, and both hand the result to core.StatusError so nothing
// about the shared contract is re-decided here.
//
// verb names the failed call ("invoke", "streaming invoke"). It is operator
// context, so it goes in the %w wrapper rather than into the typed error's
// message, which is the caller's. errors.As reaches through either way.
//
// An error with no HTTP response (network/credential failure before a response
// arrived) is returned unchanged — it genuinely has no status to report.
func bedrockInvokeError(verb string, err error) error {
	var respErr *smithyhttp.ResponseError
	if !errors.As(err, &respErr) {
		return fmt.Errorf("bedrock %s failed: %w", verb, err)
	}
	// respErr.Err is the modeled API error the SDK decoded (e.g.
	// "api error ThrottlingException: Rate exceeded"), not the raw body.
	msg := ""
	if respErr.Err != nil {
		msg = respErr.Err.Error()
	}
	statusErr := core.StatusError(Name, respErr.HTTPStatusCode(), msg).
		WithRetryAfter(respErr.Response.Header)
	// Join so the original *smithyhttp.ResponseError stays reachable via
	// errors.As alongside statusErr. The gateway classifies on statusErr (status,
	// message, Retry-After), but wrapping only statusErr dropped the AWS error
	// from the chain, hiding the RequestID and modeled fault type from anything
	// that unwraps to it.
	return fmt.Errorf("bedrock %s failed: %w", verb, errors.Join(statusErr, respErr))
}
