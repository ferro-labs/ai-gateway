package bedrock

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/ferro-labs/ai-gateway/internal/logging"
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

// warnDroppedImageParts logs a warning when a request carries image content that
// a text-only Bedrock model family (Nova/Titan/Llama on the InvokeModel path)
// cannot forward, so the drop is observable instead of silent. Preserving images
// on these families requires the Converse API (tracked as a roadmap item).
func warnDroppedImageParts(ctx context.Context, provider, model string, messages []core.Message) {
	for _, msg := range messages {
		for _, part := range msg.ContentParts {
			if part.Type == "image_url" {
				logging.FromContext(ctx).Warn(
					"model family does not support image content on the InvokeModel path; dropping image parts",
					"provider", provider,
					"model", model,
				)
				return
			}
		}
	}
}

// bedrockInvokeError translates an InvokeModel/InvokeModelWithResponseStream
// failure into a *core.HTTPStatusError when the AWS SDK received an upstream
// HTTP response (e.g. a ThrottlingException or ValidationException), so
// core.ParseStatusCode can recover the status: without it every Bedrock error
// looks like status 0, which makes a genuine 429 trip the circuit breaker
// (rate limits must not) and makes a deterministic 4xx get retried against the
// same target instead of failing fast. verb names the failed call ("invoke",
// "streaming invoke") for the message prefix. An error with no HTTP response
// (network/credential failure before a response arrived) is returned
// unchanged — it genuinely has no status to report.
func bedrockInvokeError(verb string, err error) error {
	wrapped := fmt.Errorf("bedrock %s failed: %w", verb, err)
	var respErr *smithyhttp.ResponseError
	if !errors.As(err, &respErr) {
		return wrapped
	}
	return &core.HTTPStatusError{
		StatusCode: respErr.HTTPStatusCode(),
		Message:    wrapped.Error(),
		RetryAfter: core.ParseRetryAfter(respErr.Response.Header.Get("Retry-After")),
	}
}
