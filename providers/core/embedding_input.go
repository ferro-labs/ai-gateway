package core

import (
	"errors"
	"fmt"
	"net/http"
)

// errEmptyEmbeddingInput is returned when an embeddings "input" array is empty.
var errEmptyEmbeddingInput = errors.New("embed: Input must not be an empty array")

// CoerceEmbeddingInput flattens an OpenAI embeddings "input" value into a plain
// []string for providers whose native API accepts only a list of texts. A bare
// string becomes a one-element slice; a []string is returned as-is; a []any is
// coerced element-by-element. An empty array, a nil input, or a non-string
// element is rejected.
func CoerceEmbeddingInput(input any) ([]string, error) {
	switch v := input.(type) {
	case string:
		return []string{v}, nil
	case []string:
		if len(v) == 0 {
			return nil, errEmptyEmbeddingInput
		}
		return v, nil
	case []any:
		if len(v) == 0 {
			return nil, errEmptyEmbeddingInput
		}
		return coerceAnyStrings(v)
	case nil:
		return nil, fmt.Errorf("embed: Input must not be nil")
	default:
		return nil, fmt.Errorf("embed: unsupported Input type %T; want string or []string", input)
	}
}

// NormalizeEmbeddingInput validates an OpenAI embeddings "input" value while
// preserving the bare-string vs array distinction that native/SDK request
// unions need (unlike CoerceEmbeddingInput, which always flattens to []string).
// A bare string is returned unchanged; a []string is returned as-is; a []any is
// coerced element-by-element to []string. An empty array, a nil input, or a
// non-string element is rejected. It does NOT reject empty or whitespace-only
// strings; providers that require that (azure_openai, databricks) keep their own
// stricter validator.
func NormalizeEmbeddingInput(input any) (any, error) {
	switch v := input.(type) {
	case string:
		return v, nil
	case []string:
		if len(v) == 0 {
			return nil, errEmptyEmbeddingInput
		}
		return v, nil
	case []any:
		if len(v) == 0 {
			return nil, errEmptyEmbeddingInput
		}
		return coerceAnyStrings(v)
	case nil:
		return nil, fmt.Errorf("embed: Input must not be nil")
	default:
		return nil, fmt.Errorf("embed: unsupported Input type %T; want string or []string", input)
	}
}

// ValidateEmbeddingEncodingFormat rejects an embeddings encoding_format the
// gateway cannot serve. Empty (unset) and "float" are accepted; any other
// value — "base64" included — is refused.
//
// It returns a 400-carrying *HTTPStatusError rather than a bare error, which is
// what every one of its callers used to return. A bare error carries no status,
// so internal/apierror classified it as a 500 — telling a caller who sent an
// unsupported VALUE that the gateway was broken — and strategies.shouldRetry
// reads a status-less error as a transport failure and retried it, spending the
// whole retry budget on a request whose outcome could not change.
//
// The provider name is empty because this refusal is the gateway's, not any
// upstream's; HTTPStatusError.Error() renders that case without one. The
// caller-facing text is the Message field, and it names the rejected value: for
// a value error that is the only actionable thing in the response, which is why
// this is not an UnsupportedParamError (that type carries parameter NAMES and
// would answer "encoding_format is unsupported" for a parameter that is in fact
// supported, just not at that value).
//
// "base64" is refused rather than decoded because it cannot survive this
// gateway: core.Embedding.Embedding is []float64 and internal/handler/embeddings.go
// JSON-encodes it directly, so the response leaves as a float array whatever the
// upstream sent. Decoding base64 back to floats preserves none of the bandwidth
// saving base64 exists for, while forwarding it upstream returns a vector no
// float-typed decoder can read.
func ValidateEmbeddingEncodingFormat(format string) error {
	if format != "" && format != "float" {
		return StatusError("", http.StatusBadRequest,
			fmt.Sprintf("embed: unsupported encoding_format %q; valid value is %q", format, "float"))
	}
	return nil
}

// coerceAnyStrings converts a []any of strings to []string, rejecting any
// non-string element with a positional error.
func coerceAnyStrings(v []any) ([]string, error) {
	strs := make([]string, 0, len(v))
	for i, item := range v {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("embed: Input[%d] is %T, want string", i, item)
		}
		strs = append(strs, s)
	}
	return strs, nil
}
