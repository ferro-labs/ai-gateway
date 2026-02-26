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

// NewContext creates a new plugin context for a request.
func NewContext(req *providers.Request) *Context {
	return &Context{
		Request:  req,
		Metadata: make(map[string]interface{}),
	}
}
