// Package ratelimit provides a gateway plugin that enforces per-request rate
// limits using an in-memory token bucket.  Configure it at the before_request
// stage so that over-budget requests are rejected before they hit the provider.
package ratelimit

import (
	"context"
	"fmt"

	internalrl "github.com/ferro-labs/ai-gateway/internal/ratelimit"
	"github.com/ferro-labs/ai-gateway/plugin"
)

func init() {
	plugin.RegisterFactory("rate-limit", func() plugin.Plugin {
		return &Plugin{}
	})
}

// Plugin enforces a token-bucket rate limit on incoming requests.
type Plugin struct {
	limiter *internalrl.Limiter
}

// Name returns the plugin identifier.
func (p *Plugin) Name() string { return "rate-limit" }

// Type returns the plugin lifecycle hook type.
func (p *Plugin) Type() plugin.PluginType { return plugin.TypeRateLimit }

// Init reads config keys:
//   - requests_per_second (float64 or int, default 100)
//   - burst (float64 or int, default 2× rps)
func (p *Plugin) Init(config map[string]interface{}) error {
	rps := 100.0
	burst := 0.0

	if v, ok := config["requests_per_second"]; ok {
		switch val := v.(type) {
		case float64:
			rps = val
		case int:
			rps = float64(val)
		default:
			return fmt.Errorf("rate-limit: requests_per_second must be a number")
		}
	}
	if v, ok := config["burst"]; ok {
		switch val := v.(type) {
		case float64:
			burst = val
		case int:
			burst = float64(val)
		default:
			return fmt.Errorf("rate-limit: burst must be a number")
		}
	}

	p.limiter = internalrl.New(rps, burst)
	return nil
}

// Execute rejects the request if the rate limit is exceeded.
func (p *Plugin) Execute(_ context.Context, pctx *plugin.Context) error {
	if !p.limiter.Allow() {
		pctx.Reject = true
		pctx.Reason = "rate limit exceeded"
		return fmt.Errorf("rate limit exceeded")
	}
	return nil
}
