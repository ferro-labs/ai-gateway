// Package cache provides a response-cache plugin that stores LLM responses
// in memory and serves them on exact-match cache hits, reducing provider cost
// and latency for repeated requests. Register it with a blank import:
//
//	_ "github.com/ferro-labs/ai-gateway/plugin/cache"
package cache

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"hash"
	"sort"
	"strconv"
	"time"

	cachestore "github.com/ferro-labs/ai-gateway/pkg/cache"
	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
)

func init() {
	plugin.RegisterFactory("response-cache", func() plugin.Plugin {
		return &ResponseCache{}
	})
}

// ResponseCache is a transform plugin that caches LLM responses using
// exact-match hashing of the request fields that can affect provider output.
// It aliases Memory from the internal cache package.
type ResponseCache struct {
	*cachestore.Memory
}

var _ plugin.ModelPreserving = (*ResponseCache)(nil)

// Name returns the plugin identifier.
func (c *ResponseCache) Name() string {
	return "response-cache"
}

// Type returns the plugin lifecycle hook type.
func (c *ResponseCache) Type() plugin.PluginType {
	return plugin.TypeTransform
}

// Init configures the plugin from the provided options map.
func (c *ResponseCache) Init(config map[string]any) error {
	maxAge := 300
	// JSON delivers numeric values as float64; YAML may deliver int. Handle both.
	switch v := config["max_age"].(type) {
	case int:
		maxAge = v
	case float64:
		maxAge = int(v)
	}
	ttl := time.Duration(maxAge) * time.Second

	capacity := 1000
	switch v := config["max_entries"].(type) {
	case int:
		capacity = v
	case float64:
		capacity = int(v)
	}
	c.Memory = cachestore.NewMemory(capacity, ttl)
	return nil
}

// Execute checks for a cache hit (before request) or stores the response (after
// request) and does maintenance as per LRU policy.
//
// A hit sets SkipProvider, which suppresses the provider call and nothing else:
// every guardrail, rate limit and budget registered behind this plugin still runs
// on the request, and the after_request stage still records it.
func (c *ResponseCache) Execute(_ context.Context, pctx *plugin.Context) error {
	if pctx.Request == nil {
		return nil
	}

	// Both stages stand down on a non-chat surface. The request there is a
	// PROJECTION of an embeddings input or an image prompt onto the chat request
	// shape (see plugin.MetadataSurface), so it hashes exactly as the chat
	// request asking the same question — while the response projected back
	// carries no Choices at all. Storing one would serve an empty completion to
	// the next chat request with that text, and never call the provider that
	// would have answered it.
	//
	// Nothing is lost by standing down: the gateway drops whatever this plugin
	// leaves on those surfaces, because a providers.Response cannot hold an
	// embedding or an image. Caching them needs a seam that can, not a key.
	if _, ok := pctx.Metadata[plugin.MetadataSurface]; ok {
		return nil
	}

	// The credential is part of the key, not just the request content. See
	// cacheKey. Metadata carries the opaque APIKey.ID, never the bearer secret,
	// and it is populated before the before_request stage, so both stages of one
	// request read the same value.
	apiKey, _ := pctx.Metadata["api_key"].(string)
	key := cacheKey(pctx.Request, apiKey)

	if pctx.Stage == plugin.StageBeforeRequest {
		if resp, ok := c.Get(key); ok {
			pctx.Response = cloneResponse(resp)
			pctx.SkipProvider = true
			pctx.Metadata["cache_hit"] = true
		}
		return nil
	}

	// after_request: store. A response this plugin just served is already in the
	// cache; re-storing it would refresh a TTL the operator set from the original
	// fetch.
	if pctx.SkipProvider || pctx.Response == nil {
		return nil
	}

	if c.Capacity <= 0 {
		return nil
	}

	// Store a private copy: the caller's resp keeps being mutated after this
	// call returns (e.g. Route/RouteStream stamp OverheadMs post-RunAfter), so
	// the cache must not hold onto the same pointer.
	c.Set(key, cloneResponse(pctx.Response))
	return nil
}

// PreservesModel declares that this plugin never changes the model a request
// routes to — it reads the request and answers from a store, and rewrites
// nothing. See plugin.ModelPreserving: it reports TypeTransform, and that alone
// used to cost the deployment the pre-plugin admission check, so enabling
// caching let an unroutable model spend a rate-limit token and a budget on its
// way to a 404.
func (c *ResponseCache) PreservesModel() {}

// Close releases plugin resources.
func (c *ResponseCache) Close() error { return nil }

// cloneResponse returns a shallow copy of resp so each cache hit gets its own
// top-level struct. Route/RouteStream stamp Object/Created/OverheadMs on the
// response returned from a cache hit; Choices/Metadata/Usage are never
// mutated post-hit, so a shallow copy is sufficient to remove the race on a
// cache entry shared across concurrent callers.
func cloneResponse(resp *providers.Response) *providers.Response {
	clone := *resp
	return &clone
}

// cacheKey hashes every request field that can change the answer a provider
// gives, plus the credential the request was served under. The set is expected
// to GROW: a field added to providers.Request that reaches the provider belongs
// here, and adding one is safe — a longer key only separates entries that were
// previously conflated, so the worst outcome is a cold cache, while a missing
// field serves one request's answer to a different question.
//
// apiKey is the opaque APIKey.ID from Context.Metadata, never the bearer
// secret. It is in the key because the store is process-global and identical
// prompts are the common case, so keying on content alone let a response primed
// by one credential be served to another — the caller inherits an answer it was
// never authorised to receive, and the guardrails, budget and rate limits that
// would have run against its own call are all evaluated against a request that
// never reaches a provider. The gateway's other two per-request plugins already
// scope on exactly this value (plugin/ratelimit, plugin/budget); this makes the
// cache consistent with them rather than the one plugin that ignores identity.
//
// An empty apiKey — an unauthenticated request — is a value like any other, so
// unauthenticated callers share one bucket and never share with an
// authenticated one. Length-prefixed writes keep "" from colliding with any
// real ID. That mirrors the request log, where api_key_id=none deliberately
// folds every unattributable row together: with no identity there is nothing to
// separate on, and refusing to cache instead would silently disable caching for
// a whole deployment rather than isolate anything.
//
// The provider is deliberately NOT in the key. Context.Target is empty at
// before_request by design, so the target that will serve a request is unknown
// when the key is computed, and resolving it later would break the SkipProvider
// contract. The assumption this bakes in: a model id names the same model
// whichever target serves it. That is the same assumption a fallback target
// makes, so a deployment where it does not hold has a routing problem, not a
// cache problem.
func cacheKey(req *providers.Request, apiKey string) string {
	h := sha256.New()
	writeCacheKeyString(h, "api_key")
	writeCacheKeyString(h, apiKey)
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
	writeCacheKeyJSON(h, "tools", req.Tools)
	writeCacheKeyJSON(h, "tool_choice", req.ToolChoice)
	writeCacheKeyOptionalBool(h, "parallel_tool_calls", req.ParallelToolCalls)
	writeCacheKeyJSON(h, "response_format", req.ResponseFormat)
	writeCacheKeyBool(h, "logprobs", req.LogProbs)
	writeCacheKeyOptionalInt(h, "top_logprobs", req.TopLogProbs)
	writeCacheKeyBool(h, "stream", req.Stream)
	writeCacheKeyString(h, "user")
	writeCacheKeyString(h, req.User)
	writeCacheKeyFloatMap(h, "logit_bias", req.LogitBias)

	return hex.EncodeToString(h.Sum(nil))
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

func writeCacheKeyJSON(h hash.Hash, label string, v any) {
	writeCacheKeyString(h, label)
	b, err := json.Marshal(v)
	if err != nil {
		writeCacheKeyString(h, err.Error())
		return
	}
	writeCacheKeyString(h, string(b))
}

func writeCacheKeyString(h hash.Hash, s string) {
	var lenBuf [8]byte
	binary.BigEndian.PutUint64(lenBuf[:], uint64(len(s)))
	_, _ = h.Write(lenBuf[:])
	_, _ = h.Write([]byte(s))
}
