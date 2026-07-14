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
// Built-in plugins live in the internal/plugins/* packages and are registered
// by importing them with a blank import (e.g. _ "github.com/ferro-labs/ai-gateway/internal/plugins/wordfilter").
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
	// response carried by pctx. It may mutate them or set pctx.Reject / pctx.Skip
	// to influence the pipeline (see Context).
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

// Context provides access to request/response data for plugins.
type Context struct {
	Request  *providers.Request
	Response *providers.Response
	// Metadata carries key/value data shared between plugins and stages (for
	// example "api_key" or "cache_hit"). Writing Metadata never alters pipeline
	// control flow; it only passes information along.
	Metadata map[string]any
	// Error holds the provider or pipeline error surfaced to the after_request
	// and on_error stages so plugins can observe it. Setting it does not by
	// itself abort the request.
	Error error
	// Skip, when set true by a plugin, stops the remaining plugins in the current
	// stage from running. The request itself continues normally.
	Skip bool
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

// reset clears all 8 fields before returning to the pool.
// Metadata map entries are deleted but the map itself is kept
// to preserve its bucket array capacity for the next request.
// SECURITY: every field must be listed explicitly.
func (c *Context) reset() {
	c.Request = nil   // field 1: *providers.Request
	c.Response = nil  // field 2: *providers.Response
	clear(c.Metadata) // field 3: map[string]interface{} — clear entries, keep capacity
	c.Error = nil     // field 4: error
	c.Skip = false    // field 5: bool
	c.Reject = false  // field 6: bool
	c.Reason = ""     // field 7: string
	c.Span = nil      // field 8: observability.Span
}
