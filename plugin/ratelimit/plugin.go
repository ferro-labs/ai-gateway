// Package ratelimit provides a gateway plugin that enforces per-request rate
// limits using an in-memory token bucket.  Configure it at the before_request
// stage so that over-budget requests are rejected before they hit the provider.
//
// Every key below is a rate or a capacity, so every one of them must be
// positive when present; zero and negative values are rejected at load rather
// than honoured as a rate of zero that rejects all traffic. Turn the plugin off
// with `enabled: false`.
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
	"math"

	"github.com/ferro-labs/ai-gateway/pkg/metrics"
	internalrl "github.com/ferro-labs/ai-gateway/pkg/ratelimit"
	"github.com/ferro-labs/ai-gateway/plugin"
)

// defaultMaxKeys is the default maximum number of keys tracked in per-key and
// per-user rate limiter stores. When the cap is reached the least recently
// accessed entry is evicted to prevent unbounded memory growth.
const defaultMaxKeys = 100_000

// rejectionKeyType is this plugin's key_type label on
// gateway_rate_limit_rejections_total.
//
// One label covers all three buckets. Which bucket denied a request is already
// in the rejection reason the caller receives; splitting the counter would
// promise a breakdown the HTTP middleware's own label does not offer either,
// for a signal that is watched as "are we shedding load".
const rejectionKeyType = "plugin"

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

// limits are the rates one rate-limit config block resolves to. A zero keyRPM
// or userRPM means the limiter is not configured; every other field is always
// positive, because parseLimits rejects anything else.
type limits struct {
	rps     float64
	burst   float64
	keyRPM  float64
	userRPM float64
}

// parseLimits reads and checks a rate-limit config block. It is the single
// place the plugin's rules live, shared by Init and ValidateConfig so a value
// the gateway would refuse to start on is the same value `ferrogw validate`
// reports.
//
// Every rate here must be positive. Each one is a rate, not a switch: a rate of
// zero rejects every request forever, which is never what an operator writing
// it meant, and the shape of the mistake — the gateway starts, reports healthy,
// and blackholes all traffic — is the worst one a config error can take. The
// way to turn this plugin off is `enabled: false`.
//
// The unrelated per-IP limiter configured by the RATE_LIMIT_RPS environment
// variable reads 0 the other way round, as "no limiting". That is deliberate
// and documented: there the variable is the whole switch, here the field is one
// setting inside a plugin that already has one.
func parseLimits(config map[string]any) (limits, error) {
	l := limits{rps: 100.0}
	var err error

	if v, ok := config["requests_per_second"]; ok {
		if l.rps, err = positiveRate("requests_per_second", v); err != nil {
			return limits{}, err
		}
	}
	if v, ok := config["burst"]; ok {
		if l.burst, err = positiveRate("burst", v); err != nil {
			return limits{}, err
		}
	}
	if v, ok := config["key_rpm"]; ok {
		if l.keyRPM, err = positiveRate("key_rpm", v); err != nil {
			return limits{}, err
		}
	}
	if v, ok := config["user_rpm"]; ok {
		if l.userRPM, err = positiveRate("user_rpm", v); err != nil {
			return limits{}, err
		}
	}
	return l, nil
}

// positiveRate converts one configured value and rejects anything that is not a
// usable rate.
func positiveRate(key string, v any) (float64, error) {
	f, err := plugin.ToFloat64(v)
	if err != nil {
		return 0, fmt.Errorf("rate-limit: %s: %w", key, err)
	}
	if math.IsNaN(f) || math.IsInf(f, 0) || f <= 0 {
		return 0, fmt.Errorf(
			"rate-limit: %s must be > 0, got %v; it is a rate, and a rate of zero or less rejects every request forever -- set enabled: false to turn the plugin off",
			key, v)
	}
	return f, nil
}

// ValidateConfig checks the config block without building anything, so
// `ferrogw validate` and `ferrogw doctor` reject a rate this plugin would
// reject at startup. See plugin.ConfigValidator.
func (p *Plugin) ValidateConfig(config map[string]any) error {
	_, err := parseLimits(config)
	return err
}

// Init reads the plugin configuration and initialises limiters.
func (p *Plugin) Init(config map[string]any) error {
	l, err := parseLimits(config)
	if err != nil {
		return err
	}

	// burst unset leaves l.burst at 0, which internalrl.New reads as "no extra
	// burst beyond the rate".
	p.limiter = internalrl.New(l.rps, l.burst)

	// burst=rpm lets a key or user spend up to a full minute's worth of tokens
	// when idle, matching typical RPM semantics.
	if l.keyRPM > 0 {
		p.keyStore = internalrl.NewStoreWithMax(l.keyRPM/60.0, l.keyRPM, defaultMaxKeys)
	}
	if l.userRPM > 0 {
		p.userStore = internalrl.NewStoreWithMax(l.userRPM/60.0, l.userRPM, defaultMaxKeys)
	}

	return nil
}

// Execute rejects the request if any configured rate limit is exceeded.
// Checks are applied in order: global → per-key → per-user.
func (p *Plugin) Execute(_ context.Context, pctx *plugin.Context) error {
	if !p.limiter.Allow() {
		return reject(pctx, "rate limit exceeded")
	}

	if p.keyStore != nil {
		if key, ok := pctx.Metadata["api_key"].(string); ok && key != "" {
			if !p.keyStore.Allow(key) {
				return reject(pctx, "per-key rate limit exceeded")
			}
		}
	}

	if p.userStore != nil && pctx.Request != nil {
		if userID := pctx.Request.User; userID != "" {
			if !p.userStore.Allow(userID) {
				return reject(pctx, "per-user rate limit exceeded")
			}
		}
	}

	return nil
}

// reject denies the request and counts it.
//
// Counting happens here, at the single place a rejection is produced, so the
// metric cannot fall behind the behaviour: a fourth limiter added above gets its
// sample by construction. Returning nil is the plugin contract — a denial is the
// plugin working, and an error would mean the plugin itself broke.
func reject(pctx *plugin.Context, reason string) error {
	pctx.Reject = true
	pctx.Reason = reason
	metrics.RateLimitRejections.WithLabelValues(rejectionKeyType).Inc()
	return nil
}

// Close releases plugin resources.
func (p *Plugin) Close() error { return nil }
