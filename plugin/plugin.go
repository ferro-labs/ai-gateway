// Package plugin defines the Plugin interface and the lifecycle stages
// used to hook into the gateway request pipeline.
//
// Plugins are registered by name via RegisterFactory and loaded by the
// gateway at startup. The plugin.Context carries the request and response
// through each stage, and plugins may modify, reject, or skip requests.
//
// Built-in plugins live in the internal/plugins/* packages and are registered
// by importing them with a blank import (e.g. _ "github.com/ferro-labs/ai-gateway/internal/plugins/wordfilter").
package plugin

import (
	"context"
	"sync"

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
	Reject bool
	// Reason is the human-readable explanation reported to the client when
	// Reject is set.
	Reason string
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

// reset clears all 7 fields before returning to the pool.
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
}
