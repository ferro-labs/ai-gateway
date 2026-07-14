package core

import (
	"context"
	"slices"

	"github.com/ferro-labs/ai-gateway/internal/logging"
	"github.com/ferro-labs/ai-gateway/providers/capabilities"
)

// optionalParamOrder is the stable, deterministic ordering used when reporting
// dropped parameters, so log output (and tests) are reproducible.
var optionalParamOrder = []string{
	"temperature",
	"top_p",
	"n",
	"seed",
	"max_tokens",
	"max_completion_tokens",
	"presence_penalty",
	"frequency_penalty",
	"stop",
	"tools",
	"tool_choice",
	"response_format",
	"logprobs",
	"top_logprobs",
	"user",
	"logit_bias",
}

// ParamPopulated reports whether the named optional OpenAI parameter carries a
// caller-supplied value on req. Required fields (model, messages) are never
// considered optional params.
func ParamPopulated(req Request, name string) bool {
	switch name {
	case "temperature":
		return req.Temperature != nil
	case "top_p":
		return req.TopP != nil
	case "n":
		return req.N != nil
	case "seed":
		return req.Seed != nil
	case "max_tokens":
		return req.MaxTokens != nil
	case "max_completion_tokens":
		return maxCompletionTokensPopulated(req)
	case "presence_penalty":
		return req.PresencePenalty != nil
	case "frequency_penalty":
		return req.FrequencyPenalty != nil
	case "stop":
		return len(req.Stop) > 0
	case "tools":
		return len(req.Tools) > 0
	case "tool_choice":
		return req.ToolChoice != nil
	case "response_format":
		return req.ResponseFormat != nil
	case "logprobs":
		return req.LogProbs
	case "top_logprobs":
		return req.TopLogProbs != nil
	case "user":
		return req.User != ""
	case "logit_bias":
		return len(req.LogitBias) > 0
	default:
		return false
	}
}

// maxCompletionTokensPopulated reports whether max_completion_tokens still
// carries an unresolved caller value. Request.NormalizeCompletionTokenLimits
// copies MaxCompletionTokens into MaxTokens when MaxTokens is nil, so a
// caller who set only max_completion_tokens is already satisfiable through
// max_tokens by the time enforcement runs, even on a provider that cannot
// express max_completion_tokens natively — reporting it dropped there would
// be misleading. A caller who sets both fields to different values still has
// the max_completion_tokens value silently ignored (normalization never
// overwrites an explicit max_tokens), so that case is still reported.
func maxCompletionTokensPopulated(req Request) bool {
	if req.MaxCompletionTokens == nil {
		return false
	}
	return req.MaxTokens == nil || *req.MaxTokens != *req.MaxCompletionTokens
}

// DroppedParams returns, in stable order, the optional OpenAI parameters that
// the caller populated on req but that are NOT in the supported set. Native
// providers pass the list of parameters their upstream API can express; the
// remainder is what gets silently dropped without intervention.
func DroppedParams(req Request, supported ...string) []string {
	var dropped []string
	for _, name := range optionalParamOrder {
		if !slices.Contains(supported, name) && ParamPopulated(req, name) {
			dropped = append(dropped, name)
		}
	}
	return dropped
}

// WarnUnsupportedParams emits a structured warn-level log for any optional
// parameter the caller set that the target provider's native API cannot
// express, so the drop is observable instead of silent (issue #140). It is a
// no-op when nothing populated is unsupported.
//
// Deprecated: use EnforceUnsupportedParams (matrix-driven) or
// EnforceUnsupportedParamsList (explicit per-model list) instead. Both honor the
// configured compatibility mode (warn/drop/reject) rather than only warning.
func WarnUnsupportedParams(ctx context.Context, provider, model string, req Request, supported ...string) {
	dropped := DroppedParams(req, supported...)
	if len(dropped) == 0 {
		return
	}
	logging.FromContext(ctx).Warn(
		"provider does not support request parameter(s); dropping",
		"provider", provider,
		"model", model,
		"dropped_params", dropped,
	)
}

// DroppedParamsForProvider returns, in stable order, the optional OpenAI
// parameters the caller populated on req that the capability matrix declares
// Unsupported for provider. It is the matrix-driven counterpart of
// DroppedParams: the matrix (providers/capabilities) is the single source of
// provider parameter support, so native providers no longer hardcode their own
// supported lists.
func DroppedParamsForProvider(req Request, provider string) []string {
	var dropped []string
	for _, name := range optionalParamOrder {
		if capabilities.SupportOf(provider, name) == capabilities.Unsupported && ParamPopulated(req, name) {
			dropped = append(dropped, name)
		}
	}
	return dropped
}

// EnforceUnsupportedParams applies the compatibility mode carried by ctx to the
// parameters req sets that provider cannot express, per the capability matrix.
// warn and drop log the dropped parameters and return nil — native providers
// build a native payload that never forwards them, so both modes are observable
// no-ops on the request. reject returns an *UnsupportedParamError (HTTP 400)
// naming the parameters and does not proceed. It is the single enforcement seam
// for matrix-declared native providers.
func EnforceUnsupportedParams(ctx context.Context, provider, model string, req Request) error {
	return enforceUnsupportedParams(ctx, provider, model, DroppedParamsForProvider(req, provider))
}

// EnforceUnsupportedParamsList is EnforceUnsupportedParams for providers whose
// supported set is model-dependent (Bedrock): the caller passes the resolved
// per-model supported parameters instead of relying on the provider-level matrix.
func EnforceUnsupportedParamsList(ctx context.Context, provider, model string, req Request, supported ...string) error {
	return enforceUnsupportedParams(ctx, provider, model, DroppedParams(req, supported...))
}

// enforceUnsupportedParams logs (warn/drop) or rejects (reject) the given
// dropped parameters according to the compatibility mode carried by ctx. It is
// a no-op when nothing populated is unsupported.
func enforceUnsupportedParams(ctx context.Context, provider, model string, dropped []string) error {
	if len(dropped) == 0 {
		return nil
	}
	if UnsupportedParamModeFromContext(ctx) == UnsupportedParamReject {
		return NewUnsupportedParamError(provider, dropped)
	}
	logging.FromContext(ctx).Warn(
		"provider does not support request parameter(s); dropping",
		"provider", provider,
		"model", model,
		"dropped_params", dropped,
	)
	return nil
}

// UnsupportedParamMode selects how the shared request builder treats a request
// parameter the target provider cannot express. Warn is the zero value, so
// callers that never set a mode keep the historical warn-and-forward behaviour.
type UnsupportedParamMode int

const (
	// UnsupportedParamWarn logs the unsupported parameter and forwards the
	// request unchanged. It is the default (zero value).
	UnsupportedParamWarn UnsupportedParamMode = iota
	// UnsupportedParamDrop omits the unsupported parameter from the forwarded
	// upstream request and logs a warning.
	UnsupportedParamDrop
	// UnsupportedParamReject fails the request with an HTTP 400 error naming the
	// unsupported parameter.
	UnsupportedParamReject
)

// String returns the config wire name of the mode ("warn", "drop", "reject").
func (m UnsupportedParamMode) String() string {
	switch m {
	case UnsupportedParamDrop:
		return "drop"
	case UnsupportedParamReject:
		return "reject"
	default:
		return "warn"
	}
}

// ParseUnsupportedParamMode maps a config string to an UnsupportedParamMode. An
// empty string and "warn" both map to UnsupportedParamWarn. ok is false for any
// other value (the caller should treat that as a config error); the returned
// mode is UnsupportedParamWarn in that case so a misconfiguration fails safe.
func ParseUnsupportedParamMode(s string) (mode UnsupportedParamMode, ok bool) {
	switch s {
	case "", "warn":
		return UnsupportedParamWarn, true
	case "drop":
		return UnsupportedParamDrop, true
	case "reject":
		return UnsupportedParamReject, true
	default:
		return UnsupportedParamWarn, false
	}
}

// unsupportedParamModeKey is the private context key for the compatibility mode.
type unsupportedParamModeKey struct{}

// WithUnsupportedParamMode returns a context carrying mode, read by the shared
// request builder. The gateway sets it once per request from config; providers
// need no changes.
func WithUnsupportedParamMode(ctx context.Context, mode UnsupportedParamMode) context.Context {
	return context.WithValue(ctx, unsupportedParamModeKey{}, mode)
}

// UnsupportedParamModeFromContext returns the mode stored by
// WithUnsupportedParamMode, or UnsupportedParamWarn when none is set.
func UnsupportedParamModeFromContext(ctx context.Context) UnsupportedParamMode {
	if mode, ok := ctx.Value(unsupportedParamModeKey{}).(UnsupportedParamMode); ok {
		return mode
	}
	return UnsupportedParamWarn
}
