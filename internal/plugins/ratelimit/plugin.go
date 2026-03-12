// Package ratelimit provides a gateway plugin that enforces per-request rate
// limits using an in-memory token bucket.  Configure it at the before_request
// stage so that over-budget requests are rejected before they hit the provider.
//
// Supported config keys:
//   - requests_per_second (float64|int, default 100): global request rate.
//   - burst (float64|int, default = rps): global burst capacity.
//   - key_rpm (float64|int, optional): per-API-key rate limit in requests/minute.
//     The API key is read from pctx.Metadata["api_key"]. Requests without a
//     key share a global limiter and are not individually rate-limited by this
//     option.
//   - user_rpm (float64|int, optional): per-user rate limit in requests/minute.
//     The user ID is read from pctx.Request.User. Requests with an empty User
//     field are not individually limited by this option.
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

// Plugin enforces token-bucket rate limits on incoming requests.
//
// Three limiters are layered — a request is rejected as soon as any one of
// them denies it:
//  1. Global limiter (requests_per_second / burst)
//  2. Per-API-key limiter (key_rpm) — keyed on Metadata["api_key"]
//  3. Per-user limiter (user_rpm) — keyed on Request.User
type Plugin struct {
	limiter   *internalrl.Limiter // global
	keyStore  *internalrl.Store   // per API key (nil when key_rpm unset)
	userStore *internalrl.Store   // per user   (nil when user_rpm unset)
}

// Name returns the plugin identifier.
func (p *Plugin) Name() string { return "rate-limit" }

// Type returns the plugin lifecycle hook type.
func (p *Plugin) Type() plugin.PluginType { return plugin.TypeRateLimit }

// Init reads the plugin configuration and initialises limiters.
func (p *Plugin) Init(config map[string]interface{}) error {
	rps := 100.0
	burst := 0.0

	if v, ok := config["requests_per_second"]; ok {
		f, err := toFloat64(v)
		if err != nil {
			return fmt.Errorf("rate-limit: requests_per_second: %w", err)
		}
		rps = f
	}
	if v, ok := config["burst"]; ok {
		f, err := toFloat64(v)
		if err != nil {
			return fmt.Errorf("rate-limit: burst: %w", err)
		}
		burst = f
	}
	p.limiter = internalrl.New(rps, burst)

	if v, ok := config["key_rpm"]; ok {
		rpm, err := toFloat64(v)
		if err != nil {
			return fmt.Errorf("rate-limit: key_rpm: %w", err)
		}
		if rpm <= 0 {
			return fmt.Errorf("rate-limit: key_rpm must be > 0")
		}
		p.keyStore = internalrl.NewStore(rpm/60.0, rpm/60.0)
	}

	if v, ok := config["user_rpm"]; ok {
		rpm, err := toFloat64(v)
		if err != nil {
			return fmt.Errorf("rate-limit: user_rpm: %w", err)
		}
		if rpm <= 0 {
			return fmt.Errorf("rate-limit: user_rpm must be > 0")
		}
		p.userStore = internalrl.NewStore(rpm/60.0, rpm/60.0)
	}

	return nil
}

// Execute rejects the request if any configured rate limit is exceeded.
// Checks are applied in order: global → per-key → per-user.
func (p *Plugin) Execute(_ context.Context, pctx *plugin.Context) error {
	if !p.limiter.Allow() {
		pctx.Reject = true
		pctx.Reason = "rate limit exceeded"
		return fmt.Errorf("rate limit exceeded")
	}

	if p.keyStore != nil {
		if key, ok := pctx.Metadata["api_key"].(string); ok && key != "" {
			if !p.keyStore.Allow(key) {
				pctx.Reject = true
				pctx.Reason = "per-key rate limit exceeded"
				return fmt.Errorf("per-key rate limit exceeded")
			}
		}
	}

	if p.userStore != nil && pctx.Request != nil {
		if userID := pctx.Request.User; userID != "" {
			if !p.userStore.Allow(userID) {
				pctx.Reject = true
				pctx.Reason = "per-user rate limit exceeded"
				return fmt.Errorf("per-user rate limit exceeded")
			}
		}
	}

	return nil
}

// toFloat64 converts an interface{} value to float64, accepting float64 or int.
func toFloat64(v interface{}) (float64, error) {
	switch val := v.(type) {
	case float64:
		return val, nil
	case int:
		return float64(val), nil
	default:
		return 0, fmt.Errorf("must be a number, got %T", v)
	}
}
