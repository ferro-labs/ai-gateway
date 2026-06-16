// Package openaicompat builds request bodies for providers that speak the
// OpenAI Chat Completions wire format.
//
// Historically each OpenAI-compatible provider hand-rolled a local request
// struct that copied only a handful of fields off core.Request (model,
// messages, temperature, max_tokens), silently dropping every other sampling
// and output parameter — top_p, n, seed, stop, presence/frequency penalties,
// response_format, tools, logit_bias, user, logprobs. Centralising the body
// build here keeps those ~20 providers from drifting and forwards the full
// OpenAI-shaped request (issue #140).
package openaicompat

import (
	"io"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// BuildBody marshals an OpenAI-compatible chat completion request body from a
// core.Request. Every OpenAI-shaped field carried by core.Request is forwarded
// as-is; nothing is silently dropped. The stream argument sets the upstream
// "stream" flag (core.Request.Stream is omitempty, so false is omitted to match
// the previous per-provider behaviour).
//
// The returned release func MUST be called once the caller is done with the
// reader to return the pooled buffer; it mirrors core.JSONBodyReader.
func BuildBody(req core.Request, stream bool) (body io.Reader, contentLen int, release func(), err error) {
	req.Stream = stream
	return core.JSONBodyReader(req)
}
