package core

import (
	"context"
	"slices"

	"github.com/ferro-labs/ai-gateway/internal/logging"
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

// paramPopulated reports whether the named optional OpenAI parameter carries a
// caller-supplied value on req. Required fields (model, messages) are never
// considered optional params.
func paramPopulated(req Request, name string) bool {
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
		return req.MaxCompletionTokens != nil
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

// DroppedParams returns, in stable order, the optional OpenAI parameters that
// the caller populated on req but that are NOT in the supported set. Native
// providers pass the list of parameters their upstream API can express; the
// remainder is what gets silently dropped without intervention.
func DroppedParams(req Request, supported ...string) []string {
	var dropped []string
	for _, name := range optionalParamOrder {
		if !slices.Contains(supported, name) && paramPopulated(req, name) {
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
// This is warn-and-drop, not a hard failure: forwarding-only providers never
// need it because the shared openaicompat builder forwards everything. The
// supported argument lists the OpenAI parameter names the provider translates.
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
