package cache

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"math"
	"sort"
	"strconv"

	"github.com/ferro-labs/ai-gateway/providers"
)

// Fingerprint returns a versioned SHA-256 key for a chat completion request and
// an opaque caller-chosen namespace. It preserves message order and optional
// values; map insertion order does not affect the key. Nil requests and values
// that cannot be JSON encoded return an empty key and an error.
//
// Namespace scopes otherwise identical requests. Use an opaque identifier, not
// a credential. The empty namespace is shared by all callers that choose it.
// Neither the namespace nor request content appears in the returned key.
//
// The key describes completion content, not authorization or semantic similarity.
// RoutingMetadata, StreamOptions and ClientStreamOptions are excluded: callers
// must scope caches by routing configuration and handle stream framing separately.
// A model name is assumed to identify the same model across provider targets.
//
// New completion-affecting request fields must be included. Adding or changing
// encoded fields requires a new version prefix and new fixed test vectors, even
// if it only separates previously equivalent requests. This intentionally causes
// a cold cache rather than changing the meaning of an existing version. Excluded
// fields require an explicit rationale in the field-coverage test; adding an
// exclusion that leaves the encoding unchanged does not require a new prefix.
func Fingerprint(req *providers.Request, namespace string) (string, error) {
	if req == nil {
		return "", errors.New("cache fingerprint: nil request")
	}
	for _, value := range []*float64{req.Temperature, req.TopP, req.PresencePenalty, req.FrequencyPenalty} {
		if value != nil && (math.IsNaN(*value) || math.IsInf(*value, 0)) {
			return "", errors.New("cache fingerprint: non-finite sampling value")
		}
	}
	for _, value := range req.LogitBias {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return "", errors.New("cache fingerprint: non-finite logit bias")
		}
	}
	h := sha256.New()
	writeCacheKeyString(h, "namespace")
	writeCacheKeyString(h, namespace)
	writeCacheKeyString(h, "model")
	writeCacheKeyString(h, req.Model)
	writeCacheKeyString(h, "messages")
	writeCacheKeyInt(h, len(req.Messages))
	for _, m := range req.Messages {
		writeCacheKeyString(h, m.Role)
		writeCacheKeyString(h, m.Name)
		writeCacheKeyString(h, m.Content)
		writeCacheKeyContentParts(h, m.ContentParts)
		writeCacheKeyToolCalls(h, m.ToolCalls)
		writeCacheKeyString(h, m.ToolCallID)
		writeCacheKeyString(h, m.ReasoningContent)
	}
	writeCacheKeyOptionalFloat64(h, "temperature", req.Temperature)
	writeCacheKeyOptionalFloat64(h, "top_p", req.TopP)
	writeCacheKeyOptionalInt(h, "n", req.N)
	writeCacheKeyOptionalInt64(h, "seed", req.Seed)
	writeCacheKeyOptionalInt(h, "max_tokens", req.MaxTokens)
	writeCacheKeyOptionalInt(h, "max_completion_tokens", req.MaxCompletionTokens)
	writeCacheKeyOptionalFloat64(h, "presence_penalty", req.PresencePenalty)
	writeCacheKeyOptionalFloat64(h, "frequency_penalty", req.FrequencyPenalty)
	writeCacheKeyStringSlice(h, "stop", req.Stop)
	if err := writeCacheKeyJSON(h, "tools", req.Tools); err != nil {
		return "", err
	}
	if err := writeCacheKeyJSON(h, "tool_choice", req.ToolChoice); err != nil {
		return "", err
	}
	writeCacheKeyOptionalBool(h, "parallel_tool_calls", req.ParallelToolCalls)
	if err := writeCacheKeyJSON(h, "response_format", req.ResponseFormat); err != nil {
		return "", err
	}
	writeCacheKeyBool(h, "logprobs", req.LogProbs)
	writeCacheKeyOptionalInt(h, "top_logprobs", req.TopLogProbs)
	writeCacheKeyBool(h, "stream", req.Stream)
	writeCacheKeyString(h, "user")
	writeCacheKeyString(h, req.User)
	writeCacheKeyFloatMap(h, "logit_bias", req.LogitBias)

	var key [3 + sha256.Size*2]byte
	copy(key[:], "v1:")
	hex.Encode(key[3:], h.Sum(nil))
	return string(key[:]), nil
}

func writeCacheKeyBool(h hash.Hash, label string, v bool) {
	writeCacheKeyString(h, label)
	writeCacheKeyString(h, strconv.FormatBool(v))
}

// writeCacheKeyOptionalBool distinguishes unset from both settings, because the
// provider does: an absent parallel_tool_calls leaves the upstream default in
// place, which is not the same request as one that pinned it either way.
func writeCacheKeyOptionalBool(h hash.Hash, label string, v *bool) {
	writeCacheKeyString(h, label)
	if v == nil {
		writeCacheKeyString(h, "<nil>")
		return
	}
	writeCacheKeyString(h, strconv.FormatBool(*v))
}

func writeCacheKeyInt(h hash.Hash, v int) {
	writeCacheKeyString(h, strconv.Itoa(v))
}

func writeCacheKeyOptionalInt(h hash.Hash, label string, v *int) {
	writeCacheKeyString(h, label)
	if v == nil {
		writeCacheKeyString(h, "<nil>")
		return
	}
	writeCacheKeyString(h, strconv.Itoa(*v))
}

func writeCacheKeyOptionalInt64(h hash.Hash, label string, v *int64) {
	writeCacheKeyString(h, label)
	if v == nil {
		writeCacheKeyString(h, "<nil>")
		return
	}
	writeCacheKeyString(h, strconv.FormatInt(*v, 10))
}

func writeCacheKeyOptionalFloat64(h hash.Hash, label string, v *float64) {
	writeCacheKeyString(h, label)
	if v == nil {
		writeCacheKeyString(h, "<nil>")
		return
	}
	writeCacheKeyString(h, strconv.FormatFloat(*v, 'g', -1, 64))
}

func writeCacheKeyStringSlice(h hash.Hash, label string, values []string) {
	writeCacheKeyString(h, label)
	writeCacheKeyInt(h, len(values))
	for _, value := range values {
		writeCacheKeyString(h, value)
	}
}

// writeCacheKeyContentParts hashes a multipart message body. Message.UnmarshalJSON
// collapses only text parts into Content, so an image part reaches the provider
// — and changes the answer — without leaving any trace in Content.
//
// Every part writes the same number of values so a part's fields can never shift
// into the next part's.
func writeCacheKeyContentParts(h hash.Hash, parts []providers.ContentPart) {
	writeCacheKeyInt(h, len(parts))
	for _, part := range parts {
		writeCacheKeyString(h, part.Type)
		writeCacheKeyString(h, part.Text)
		var url, detail string
		if part.ImageURL != nil {
			url, detail = part.ImageURL.URL, part.ImageURL.Detail
		}
		writeCacheKeyString(h, url)
		writeCacheKeyString(h, detail)
	}
}

// writeCacheKeyToolCalls hashes the tool calls carried by a message. An assistant
// turn that called one tool and one that called another are different
// conversations, and the follow-up answer differs with them.
func writeCacheKeyToolCalls(h hash.Hash, calls []providers.ToolCall) {
	writeCacheKeyInt(h, len(calls))
	for _, call := range calls {
		writeCacheKeyOptionalInt(h, "index", call.Index)
		writeCacheKeyString(h, call.ID)
		writeCacheKeyString(h, call.Type)
		writeCacheKeyString(h, call.Function.Name)
		writeCacheKeyString(h, call.Function.Arguments)
	}
}

func writeCacheKeyFloatMap(h hash.Hash, label string, values map[string]float64) {
	writeCacheKeyString(h, label)
	writeCacheKeyInt(h, len(values))
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		writeCacheKeyString(h, key)
		writeCacheKeyString(h, strconv.FormatFloat(values[key], 'g', -1, 64))
	}
}

func writeCacheKeyJSON(h hash.Hash, label string, v any) error {
	writeCacheKeyString(h, label)
	b, err := json.Marshal(v)
	if err != nil {
		return errors.New("cache fingerprint: cannot encode " + label)
	}
	writeCacheKeyString(h, string(b))
	return nil
}

func writeCacheKeyString(h hash.Hash, s string) {
	var lenBuf [8]byte
	binary.BigEndian.PutUint64(lenBuf[:], uint64(len(s)))
	_, _ = h.Write(lenBuf[:])
	_, _ = h.Write([]byte(s))
}
