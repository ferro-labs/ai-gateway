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
	Name() string
	Type() PluginType
	Init(config map[string]interface{}) error
	Execute(ctx context.Context, pctx *Context) error
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
	Metadata map[string]interface{}
	Error    error
	Skip     bool
	Reject   bool
	Reason   string
}

// pluginContextPool recycles Context objects to reduce GC pressure.
// Every request through the gateway that has plugins registered allocates
// one of these — pooling eliminates that allocation from the hot path.
var pluginContextPool = sync.Pool{
	New: func() interface{} {
		return &Context{
			Metadata: make(map[string]interface{}, 8),
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
