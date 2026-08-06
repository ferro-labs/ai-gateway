// Package plugin defines the Plugin interface and the lifecycle stages
// used to hook into the gateway request pipeline.
//
// Plugins are registered by name via RegisterFactory and loaded by the
// gateway at startup. The plugin.Context carries the request and response
// through each stage, and plugins may modify, reject, or skip requests.
//
// # Rejection and failure
//
// A plugin can end a request two ways, and the gateway treats them differently:
//
//   - Rejection — the plugin ran and DECIDED to deny the request, by setting
//     Context.Reject. This is always honoured, for every plugin type, and reaches
//     the client as a RejectionError: a 429 for a rate-limit plugin, a 4xx for
//     anything else.
//   - Failure — the plugin BROKE: it returned an error or panicked, so it never
//     reached a decision. Guardrail, auth, ratelimit, transform, and any unknown
//     plugin type fail closed, aborting the request with a FailureError, which the
//     client sees as a 500. Logging and metrics plugins fail open — a dead log sink
//     watches the request, it does not gate it — so their errors are logged and the
//     request proceeds.
//
// The distinction matters at the wire: a rate-limit plugin whose backend is down has
// not rate-limited anyone, and answering 429 would invite every SDK to retry into
// the outage. It is a server fault, and the gateway reports it as one.
//
// Note that an after_request plugin on a streaming response runs once the stream has
// already been delivered chunk by chunk. It can observe the completed response, but
// it cannot unsend it — a guardrail that must withhold content has to run at
// before_request, or the caller must not stream.
//
// # Stages and short-circuiting
//
// Skipping the provider and skipping the rest of the chain are different acts, and
// a plugin can only do the first:
//
//   - Context.SkipProvider says "do not call the provider; serve Context.Response
//     instead". Every remaining before_request plugin still runs, and so does
//     after_request. A cache hit therefore no longer disables the rate limiter or
//     the budget registered behind it.
//   - Only a rejection or a failure ends the before_request chain, and it ends that
//     stage alone: the on_error stage still runs, so a request denied by policy is
//     still recorded by whatever observes requests.
//
// SkipProvider stays set through after_request, where it is a fact rather than a
// control signal: it is how a plugin tells a request served from cache from one that
// reached a provider. Cost recording keys off it; logging deliberately does not.
//
// Context.Stage names the stage currently executing. Branch on it. Do not infer the
// stage from which fields happen to be nil — a before_request plugin can now observe
// a Response an earlier plugin put there.
//
// # Removed in v1.4.0
//
// Context.Skip is gone. It meant "abandon every plugin after me", which let a cache
// hit bypass every guardrail behind it. Code that sets or reads it no longer
// compiles; the replacement is SkipProvider, whose contract is above.
//
// Built-in plugins live in the plugin/* subpackages and are registered
// by importing them with a blank import (e.g. _ "github.com/ferro-labs/ai-gateway/plugin/wordfilter").
package plugin

import (
	"context"
	"sync"

	"github.com/ferro-labs/ai-gateway/observability"
	"github.com/ferro-labs/ai-gateway/providers"
)

// Plugin is the interface all plugins must implement.
type Plugin interface {
	// Name returns the plugin's unique registered name.
	Name() string
	// Type reports the plugin's category (guardrail, logging, transform, ...).
	Type() PluginType
	// Init configures the plugin from its config map before first use.
	Init(config map[string]any) error
	// Execute runs the plugin for the current stage against the request and
	// response carried by pctx. It may mutate them or set pctx.Reject /
	// pctx.SkipProvider to influence the pipeline (see Context).
	//
	// Return an error only when the plugin could not do its job. To deny a request,
	// set pctx.Reject — that is a verdict, and the gateway reports it as one. An
	// error means the plugin broke, and for every type but logging and metrics it
	// aborts the request as a server-side failure. See the package documentation.
	Execute(ctx context.Context, pctx *Context) error
	// Close releases resources owned by the plugin. Implementations should be
	// safe to close more than once across reload and shutdown paths.
	Close() error
}

// PluginType categorizes plugins.
//
//nolint:revive // keep for backwards compatibility
type PluginType string

// PluginType constants define the supported lifecycle attachment points.
const (
	TypeGuardrail PluginType = "guardrail"
	TypeLogging   PluginType = "logging"
	TypeMetrics   PluginType = "metrics"
	TypeAuth      PluginType = "auth"
	TypeTransform PluginType = "transform"
	TypeRateLimit PluginType = "ratelimit"
)

// Stage defines when a plugin runs in the request lifecycle.
type Stage string

// Stage constants define the execution phases within the proxy pipeline.
const (
	StageBeforeRequest Stage = "before_request"
	StageAfterRequest  Stage = "after_request"
	StageOnError       Stage = "on_error"
)

// Metadata keys the gateway itself sets on Context.Metadata. A plugin reads one
// to learn something about the request that Request cannot express; the gateway
// never reads them back, so writing one alters no control flow.
const (
	// MetadataSurface names the OpenAI surface a request arrived on —
	// "embeddings" or "images". It is absent on chat and on streaming chat.
	//
	// Those surfaces carry content Context cannot hold natively, so the gateway
	// PROJECTS it into Request.Messages: an embeddings input and an image prompt
	// become user messages, which is what lets a content guardrail screen them
	// at all. A plugin that keys on the request — a cache above all — must treat
	// this as part of the request's identity, because the projection is
	// byte-identical to the chat request asking the same question while the
	// response it produces is not a chat response.
	MetadataSurface = "surface"

	// MetadataUninspectableContent is set true when such a surface carried
	// content the gateway could not project as text: an embeddings input given
	// as token IDs, which is a lossless encoding of the exact text a content
	// policy screens.
	//
	// A content guardrail must read it as content it was REQUIRED to inspect and
	// could not, and deny the request — Reject, which is a verdict, never an
	// error, which would report the gateway as broken. Serving it instead leaves
	// the blocklist evadable by a one-line client-side transform.
	//
	// It is absent whenever the content was projected, and absent on every chat
	// request, so a deployment running no content guardrail keeps serving
	// token-id inputs exactly as before.
	MetadataUninspectableContent = "content_uninspectable"
)

// ModelPreserving is implemented by a transform plugin that never changes which
// MODEL a request routes to.
//
// The gateway refuses a model no configured target serves BEFORE the
// before_request stage, so an unroutable model cannot spend a rate limiter's
// tokens or a budget's money on its way to its 404. That check must stand down
// when the stage can still rewrite the model — and "reports TypeTransform" was
// the only signal available, which handed the stand-down to every transform
// including the ones that rewrite nothing. Enabling the response cache silently
// gave up the protection, process-wide, on all four surfaces.
//
// Declaring this narrows the signal to the question actually being asked: not
// "is this a transform" but "may this plugin change the routed model". Declaring
// it while mutating Request.Model is a bug — the gateway will then answer 404
// for a model this plugin was about to make routable.
//
// It is an opt-OUT, not an opt-in: a transform that says nothing keeps the
// stand-down, so an out-of-tree plugin that does rewrite the model behaves
// exactly as it does today.
type ModelPreserving interface {
	Plugin
	// PreservesModel is a marker; its presence is the whole declaration.
	PreservesModel()
}

// ContentAgnostic is implemented by a before_request guardrail that reaches the
// same verdict whether or not it can READ the request's content.
//
// A surface that cannot show a guardrail the content it would screen has to
// refuse rather than read the guardrail's empty pass as consent — see
// Manager.HasBeforeRequestGuardrail. TypeGuardrail was the only signal
// available, and it names a plugin's ENFORCEMENT ROLE, not what it reads, so a
// deployment running only `max-token` had uninspectable pass-through bodies
// refused although nothing would have inspected them.
//
// Declaring this narrows the signal to the question actually being asked: not
// "is this a guardrail" but "would this guardrail's approval be vacuous if the
// content could not be read".
//
// It is an opt-OUT, and the polarity is the whole point. A guardrail that says
// nothing is assumed to read content and still triggers the refusal, so an
// out-of-tree content guardrail — which cannot know to declare anything — is
// never silently reclassified into forwarding an unscreened body upstream under
// the gateway's own credential. Over-refusal is recoverable; that is not.
//
// Unlike ModelPreserving this is a QUESTION rather than a bare marker, because
// the honest answer can depend on configuration: max-token reads no content
// until `max_input_length` is set, at which point its verdict is a measurement
// of the content and an unreadable body would satisfy it at length zero.
type ContentAgnostic interface {
	Plugin
	// IgnoresRequestContent reports whether this plugin, AS CONFIGURED, reaches
	// its verdict without reading the request's content. Answering true while
	// deriving a verdict from Request.Messages is a bug: the gateway will then
	// serve a request this plugin never actually screened.
	IgnoresRequestContent() bool
}

// Context provides access to request/response data for plugins.
type Context struct {
	Request  *providers.Request
	Response *providers.Response
	// Metadata carries key/value data shared between plugins and stages (for
	// example "api_key" or "cache_hit"). Writing Metadata never alters pipeline
	// control flow; it only passes information along. The keys the gateway
	// itself sets are documented as MetadataSurface and friends.
	//
	// The map is valid for THIS request only. Contexts are pooled, and the map
	// object is reused across requests with its entries cleared, so a plugin
	// that keeps the map itself past its Execute call will be reading another
	// caller's data — including that caller's "api_key". Copy what you need to
	// keep; never retain the map.
	Metadata map[string]any
	// Error holds the provider or pipeline error surfaced to the after_request
	// and on_error stages so plugins can observe it. Setting it does not by
	// itself abort the request.
	Error error
	// Stage is the lifecycle stage currently executing, set by the framework
	// before every Execute call. Branch on it rather than inferring the stage
	// from which fields are nil: a before_request plugin can observe a Response
	// an earlier plugin in the same stage supplied.
	Stage Stage
	// Target is the routing target the gateway used: the virtual key of the
	// target that served the request or, when the request failed, the last one
	// attempted. Set by the gateway before the after_request and on_error
	// stages; empty at before_request, and empty afterwards only when no target
	// was ever attempted — a request a plugin denied, a model no configured
	// target serves, or a response served from cache (see SkipProvider).
	//
	// It names the TARGET, not the provider's opinion of itself. A target's
	// virtual key is the name its provider is registered under, so on a success
	// it is the same string Response.Provider carries; on a failure there is no
	// response to read a provider from, which is why this exists. A record of a
	// failed request that cannot say which provider failed is missing most of
	// its diagnostic value.
	//
	// Under retry and fallback one request may touch several targets. This is
	// the LAST one attempted, which is what every other record of the same
	// failure already names — the per-provider error counter, the span's target
	// key, the failed lifecycle event, and the target quoted in the error
	// itself. One request produces one outcome and one row; a list here would be
	// the single field that disagreed with all of them.
	Target string
	// SkipProvider, when set true by a before_request plugin, means the provider
	// must not be called and Response is served in its place. It never stops
	// another plugin from running — every remaining before_request plugin and
	// the whole after_request stage still execute, so a cache hit cannot bypass
	// a guardrail, a rate limit or a budget listed behind it.
	//
	// It survives into after_request as a fact about the request: true means no
	// provider was contacted. A plugin that bills for provider work must check
	// it; a plugin that records that the request happened must not.
	SkipProvider bool
	// Reject, when set true, aborts the request and returns a rejection error to
	// the client. Reason supplies the human-readable cause.
	//
	// It is honoured for every plugin type, including logging and metrics: a plugin
	// sets Reject only when it has decided to deny the request, and a decision the
	// gateway silently discarded would be worse than one it never allowed.
	Reject bool
	// Reason is the human-readable explanation reported to the client when
	// Reject is set.
	Reason string
	// Span is the request root observability span, supplied by the gateway.
	// When non-nil the plugin manager opens one child span per plugin
	// invocation through it (recording outcome and redacted errors via the
	// observability seam); when nil no plugin spans are emitted. Setting it
	// never alters pipeline control flow.
	Span observability.Span
	// Measurements carries what the gateway has measured about this request.
	// Populated before the after_request stage; zero at before_request, where
	// there is nothing to have measured yet. Setting it never alters pipeline
	// control flow.
	Measurements Measurements
}

// Measurements are the per-request numbers the gateway computes and hands to
// the after_request stage.
//
// Each optional value is paired with a flag rather than leaning on zero,
// because zero is a legitimate reading for all of them and "not applicable" is
// a different fact: a non-streaming request has no time to first token at all,
// and a model the catalog does not price has an unknown cost rather than a free
// one. A plugin that persists these should keep the distinction.
type Measurements struct {
	// DurationMs is the end-to-end time spent on the request, measured just
	// before this stage runs — so it excludes the after_request plugins
	// themselves. A latency that included the plugin recording it would be
	// measuring the observer.
	DurationMs float64
	// TTFTMs is the time to first token, in milliseconds. HasTTFT is false for
	// non-streaming requests.
	TTFTMs  float64
	HasTTFT bool
	// CostUSD is the catalog's estimate for this request. HasCost is false when
	// the catalog does not price the model.
	CostUSD float64
	HasCost bool
}

// pluginContextPool recycles Context objects to reduce GC pressure.
// Every request through the gateway that has plugins registered allocates
// one of these — pooling eliminates that allocation from the hot path.
var pluginContextPool = sync.Pool{
	New: func() any {
		return &Context{
			Metadata: make(map[string]any, 8),
		}
	},
}

// NewContext retrieves a plugin context from the pool and sets the request.
// Caller MUST call PutContext when the request is complete.
func NewContext(req *providers.Request) *Context {
	c := pluginContextPool.Get().(*Context)
	c.Request = req
	return c
}

// PutContext returns a plugin context to the pool after resetting all fields.
func PutContext(c *Context) {
	if c == nil {
		return
	}
	c.reset()
	pluginContextPool.Put(c)
}

// reset clears all 11 fields before returning to the pool.
// Metadata map entries are deleted but the map itself is kept
// to preserve its bucket array capacity for the next request.
// SECURITY: every field must be listed explicitly.
func (c *Context) reset() {
	c.Request = nil                 // field 1: *providers.Request
	c.Response = nil                // field 2: *providers.Response
	clear(c.Metadata)               // field 3: map[string]interface{} — clear entries, keep capacity
	c.Error = nil                   // field 4: error
	c.Stage = ""                    // field 5: Stage
	c.Target = ""                   // field 6: string
	c.SkipProvider = false          // field 7: bool
	c.Reject = false                // field 8: bool
	c.Reason = ""                   // field 9: string
	c.Span = nil                    // field 10: observability.Span
	c.Measurements = Measurements{} // field 11: Measurements
}
