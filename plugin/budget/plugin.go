// Package budget provides a gateway plugin that enforces per-API-key USD spend
// limits using in-memory accumulation.
//
// # Design
//
// Spend is tracked in a shared, process-level store keyed by a "store_id"
// config value (default "default"). Two plugin instances with the same
// store_id share the same accumulated spend data, which is the expected
// configuration when the plugin is registered at both request lifecycle stages:
//
//   - before_request: checks whether the API key has remaining budget;
//     rejects the request with HTTP 402 Payment Required and an
//     insufficient_quota error if the committed spend is at or over the limit.
//     This is a read-only SOFT-cap check (no reservation). It is 402 and not
//     429 because waiting does not restore a spend cap: only cost roll-off or
//     an explicit reset clears it, so a retry hint would send every SDK into a
//     backoff schedule it was always going to exhaust.
//   - after_request:  records the cost of the completed request via an atomic
//     increment so that future before_request checks see up-to-date spend.
//
// # Soft cap
//
// The limit is a SOFT cap: a bounded number of concurrently in-flight requests
// for the same key may all pass the check and collectively exceed the limit by
// their actual (post-hoc) costs. A hard cap via pre-authorization/reservation
// is intentionally out of scope — see checkBudget for the rationale (no
// reservation means no leak and no false concurrent rejection).
//
// # Configuration
//
// name: budget
// stage: before_request   # or after_request
// enabled: true
// config:
//
//	store_id: "default"             # shared ID between before/after instances
//	spend_limit_usd: 50.0           # max cumulative spend per API key (USD)
//	input_per_m_tokens: 3.0         # cost per 1M prompt tokens (USD)
//	output_per_m_tokens: 15.0       # cost per 1M completion tokens (USD)
//	cache_read_per_m_tokens: 0.30   # optional: cost per 1M cached prompt tokens
//	cache_write_per_m_tokens: 3.75  # optional: cost per 1M cache-write tokens
//	max_keys: 10000                 # max tracked keys per store; evicts min-spend key at cap
//
// # Prompt caching
//
// A provider reports a cached prompt as PromptTokens INCLUSIVE of
// CacheReadTokens — the OpenAI convention, which providers/core normalizes onto
// every provider. Setting cache_read_per_m_tokens bills the cached subset at
// that rate and the remainder at input_per_m_tokens, so it is not paid for
// twice. Leaving it unset bills the whole prompt at the input rate: an unknown
// rate must not silently bill as zero, and a visible over-report is safer for a
// spend cap than a silent under-report. This is the rule models.Calculate
// applies to a catalog entry carrying no cache price.
//
// CacheWriteTokens are reported OUTSIDE PromptTokens and are billed only when
// cache_write_per_m_tokens is set; unset, they cost nothing here.
//
// # What this plugin does not cost
//
// The model is two rates over prompt and completion tokens (plus the two
// optional cache rates). Work billed on any other dimension is not represented,
// and the shortfall is always an under-report:
//
//   - Image generation billed per image or per tile. Most image providers
//     report no token usage at all, so such a request accrues $0 here and
//     image-only traffic never exhausts a budget. Only the token-billed image
//     models (the gpt-image family) accrue anything.
//   - Audio input (per minute) and audio output (per character).
//
// models.Calculate prices all of these against the model catalog; this plugin
// deliberately prices against the operator's own configured rates instead, so
// it has no catalog to read them from.
//
// # Memory and retention
//
// All spend data is in-memory and does not survive process restarts. This
// makes the budget plugin suitable for session-scoped soft limits and
// development quotas. Durable, cross-restart billing enforcement needs spend
// state in a shared store, which this plugin deliberately does not implement.
//
// The store caps tracked keys at max_keys (default 10,000). When the cap is
// reached on a new key insertion, the key with the lowest accumulated spend is
// evicted to make room — and an evicted key restarts at $0, so under a churn of
// more than max_keys distinct keys a key's accumulated spend can be discarded
// and its cap silently reset. Raise max_keys above the number of keys in
// circulation if that matters. Use [ResetStore] or [ResetStoreKey] for explicit
// cleanup, e.g. on API key rotation or periodic housekeeping.
//
// The API key is read from pctx.Metadata["api_key"]. Requests without a key
// are not subject to per-key spend tracking (they will not be rejected by
// this plugin).
package budget

import (
	"context"
	"fmt"
	"math"
	"sync"

	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
)

func init() {
	plugin.RegisterFactory("budget", func() plugin.Plugin {
		return &Plugin{}
	})
}

// defaultMaxKeys is the default cap on the number of API keys tracked per store.
const defaultMaxKeys = 10_000

// globalStores is the process-level registry of spend stores, keyed by store_id.
var globalStores sync.Map // map[string]*spendStore

// spendStore accumulates per-key committed USD spend with an optional key
// count cap. All access is serialized through mu so that the read in
// checkBudget and the read-modify-write in add never interleave.
type spendStore struct {
	mu      sync.Mutex
	spend   map[string]float64 // api_key -> committed USD
	maxKeys int                // 0 = unlimited
}

// evictMinLocked removes the key with the lowest committed spend to make
// room for a new key.  Must be called with s.mu held.
func (s *spendStore) evictMinLocked(newKey string) {
	if _, exists := s.spend[newKey]; !exists && s.maxKeys > 0 && len(s.spend) >= s.maxKeys {
		minKey, minVal := "", math.MaxFloat64
		for k, v := range s.spend {
			if v < minVal {
				minKey, minVal = k, v
			}
		}
		if minKey != "" {
			delete(s.spend, minKey)
		}
	}
}

// add records usd worth of committed spend for key as a single atomic
// read-modify-write under the store mutex. Concurrent completions for the
// same key therefore never lose an increment (no lost-update race).
func (s *spendStore) add(key string, usd float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evictMinLocked(key)
	s.spend[key] += usd
}

func (s *spendStore) get(key string) float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.spend[key]
}

// reset removes the committed spend record for a single key.
func (s *spendStore) reset(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.spend, key)
}

// resetAll clears all committed spend records in the store.
func (s *spendStore) resetAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.spend = make(map[string]float64)
}

func getStore(id string, maxKeys int) *spendStore {
	v, _ := globalStores.LoadOrStore(id, &spendStore{
		spend:   make(map[string]float64),
		maxKeys: maxKeys,
	})
	return v.(*spendStore) //nolint:forcetypeassert // globalStores only ever holds *spendStore values
}

// ResetStoreKey removes the accumulated spend for apiKey from the named store.
// This can be used after API key rotation or for operational housekeeping.
func ResetStoreKey(storeID, apiKey string) {
	v, ok := globalStores.Load(storeID)
	if !ok {
		return
	}
	v.(*spendStore).reset(apiKey) //nolint:forcetypeassert // globalStores only ever holds *spendStore values
}

// ResetStore clears all accumulated spend for every key in the named store.
func ResetStore(storeID string) {
	v, ok := globalStores.Load(storeID)
	if !ok {
		return
	}
	v.(*spendStore).resetAll() //nolint:forcetypeassert // globalStores only ever holds *spendStore values
}

// Plugin enforces per-API-key USD spend limits.
//
// It handles both lifecycle stages in a single Execute method:
//   - Before the LLM call (pctx.Response == nil): check accumulated spend
//     against spend_limit_usd and reject if over budget.
//   - After the LLM call (pctx.Response != nil): calculate and record cost.
type Plugin struct {
	storeID          string
	spendLimitUSD    float64 // 0 = unlimited
	inputPerMTokens  float64
	outputPerMTokens float64
	// nil = the operator priced no cache rate. Distinct from 0.0, which prices
	// cached tokens as free; see the package documentation.
	cacheReadPerMTokens  *float64
	cacheWritePerMTokens *float64
	store                *spendStore
}

// Name returns the plugin identifier.
func (p *Plugin) Name() string { return "budget" }

// Type returns the plugin lifecycle hook type.
//
// TypeRateLimit names this plugin's enforcement role — it gates a request on a
// quota and fails closed — and not the status its denial carries, which is 402
// and is decided by name in internal/apierror. Do not move this to another type
// to change the status: PluginType also decides what fails open, whether a
// transform may rewrite the routed model, and whether a guardrail is assumed to
// read request content.
func (p *Plugin) Type() plugin.PluginType { return plugin.TypeRateLimit }

// settings are the values one budget config block resolves to.
type settings struct {
	storeID              string
	spendLimitUSD        float64
	inputPerMTokens      float64
	outputPerMTokens     float64
	cacheReadPerMTokens  *float64
	cacheWritePerMTokens *float64
	maxKeys              int
}

// parseBudget reads and checks a budget config block. It is the single place
// the plugin's rules live, shared by Init and ValidateConfig so a value the
// gateway would refuse to start on is the same value `ferrogw validate`
// reports. It touches nothing outside the map it is handed — the spend store is
// opened by Init alone.
func parseBudget(config map[string]any) (settings, error) {
	s := settings{storeID: "default", maxKeys: defaultMaxKeys}

	if v, ok := config["store_id"].(string); ok && v != "" {
		s.storeID = v
	}

	if v, ok := config["spend_limit_usd"]; ok {
		f, err := plugin.ToFloat64(v)
		if err != nil {
			return settings{}, fmt.Errorf("budget: spend_limit_usd: %w", err)
		}
		if f < 0 {
			return settings{}, fmt.Errorf("budget: spend_limit_usd must be >= 0")
		}
		s.spendLimitUSD = f
	}

	if v, ok := config["input_per_m_tokens"]; ok {
		f, err := plugin.ToFloat64(v)
		if err != nil {
			return settings{}, fmt.Errorf("budget: input_per_m_tokens: %w", err)
		}
		s.inputPerMTokens = f
	}

	if v, ok := config["output_per_m_tokens"]; ok {
		f, err := plugin.ToFloat64(v)
		if err != nil {
			return settings{}, fmt.Errorf("budget: output_per_m_tokens: %w", err)
		}
		s.outputPerMTokens = f
	}

	// Optional, and their ABSENCE is meaningful: an unset cache rate leaves the
	// cached subset on the input rate, a rate of 0 makes it free. See the
	// package documentation.
	if v, ok := config["cache_read_per_m_tokens"]; ok {
		f, err := plugin.ToFloat64(v)
		if err != nil {
			return settings{}, fmt.Errorf("budget: cache_read_per_m_tokens: %w", err)
		}
		if f < 0 {
			return settings{}, fmt.Errorf("budget: cache_read_per_m_tokens must be >= 0")
		}
		s.cacheReadPerMTokens = &f
	}

	if v, ok := config["cache_write_per_m_tokens"]; ok {
		f, err := plugin.ToFloat64(v)
		if err != nil {
			return settings{}, fmt.Errorf("budget: cache_write_per_m_tokens: %w", err)
		}
		if f < 0 {
			return settings{}, fmt.Errorf("budget: cache_write_per_m_tokens must be >= 0")
		}
		s.cacheWritePerMTokens = &f
	}

	if v, ok := config["max_keys"]; ok {
		n, err := plugin.ToFloat64(v)
		if err != nil {
			return settings{}, fmt.Errorf("budget: max_keys: %w", err)
		}
		if n < 0 {
			return settings{}, fmt.Errorf("budget: max_keys must be >= 0")
		}
		s.maxKeys = int(n)
	}

	if s.spendLimitUSD > 0 && s.inputPerMTokens == 0 && s.outputPerMTokens == 0 &&
		rate(s.cacheReadPerMTokens) == 0 && rate(s.cacheWritePerMTokens) == 0 {
		return settings{}, fmt.Errorf("budget: spend_limit_usd is set but every configured rate is 0; cost will always be 0 and the budget limit will never be enforced")
	}

	return s, nil
}

// ValidateConfig checks the config block without opening a spend store, so
// `ferrogw validate` and `ferrogw doctor` reject a budget block this plugin
// would reject at startup. See plugin.ConfigValidator.
func (p *Plugin) ValidateConfig(config map[string]any) error {
	_, err := parseBudget(config)
	return err
}

// Init reads the plugin configuration.
func (p *Plugin) Init(config map[string]any) error {
	s, err := parseBudget(config)
	if err != nil {
		return err
	}

	p.storeID = s.storeID
	p.spendLimitUSD = s.spendLimitUSD
	p.inputPerMTokens = s.inputPerMTokens
	p.outputPerMTokens = s.outputPerMTokens
	p.cacheReadPerMTokens = s.cacheReadPerMTokens
	p.cacheWritePerMTokens = s.cacheWritePerMTokens
	p.store = getStore(s.storeID, s.maxKeys)
	return nil
}

// Execute checks or records spend depending on the pipeline stage.
//
// At before_request it checks the accumulated spend for the API key and rejects
// the request if the limit is exceeded. At after_request it calculates the cost of
// the completed request from token usage and adds it to the store — unless no
// provider was contacted, in which case there is nothing to bill.
//
// The stage comes from pctx.Stage. It used to be inferred from pctx.Response being
// non-nil, which a cache hit makes true before the request has happened at all.
func (p *Plugin) Execute(_ context.Context, pctx *plugin.Context) error {
	key, ok := pctx.Metadata["api_key"].(string)
	if !ok || key == "" {
		// No API key in context — skip per-key budget tracking.
		return nil
	}

	if pctx.Stage == plugin.StageBeforeRequest {
		return p.checkBudget(pctx, key)
	}

	// after_request stage: record cost from the completed request's usage.
	//
	// Unless there was no request to bill. SkipProvider means no provider was
	// contacted — a response served from cache — and charging a key for a call
	// that never left the process is how one prompt repeated a hundred times
	// billed a hundred times against a limit it should never have touched.
	if pctx.SkipProvider {
		return nil
	}
	p.recordCost(pctx, key)
	return nil
}

// usageFromContext returns the completed request's token usage — from the chat
// Response or, for non-chat surfaces, Metadata["usage"] (the sanctioned additive
// channel for the frozen plugin seam).
//
// ok is false only when neither channel carries usage at all. It is NOT the
// image signal: the images surface now projects a Response (see
// Gateway.surfaceRecord.pluginView), so an image request returns ok=true with
// zero tokens and is therefore gated but costs nothing — see "What this plugin
// does not cost" in the package documentation.
func usageFromContext(pctx *plugin.Context) (providers.Usage, bool) {
	if pctx.Response != nil {
		return pctx.Response.Usage, true
	}
	if u, ok := pctx.Metadata["usage"].(providers.Usage); ok {
		return u, true
	}
	return providers.Usage{}, false
}

// Close releases plugin resources.
func (p *Plugin) Close() error { return nil }

// checkBudget is a read-only soft-cap check.
//
// # Soft cap semantics
//
// This plugin enforces a SOFT spend cap. The before_request check only reads
// the already-committed spend for the key; it places no reservation. A bounded
// number of requests for the same key may be in flight simultaneously, all
// observing a committed spend below the limit, and may collectively push the
// committed total past the limit by their actual (post-hoc) costs once each
// completes. The overshoot is bounded by the number of concurrently in-flight
// requests times their per-request cost — it is not unbounded.
//
// A HARD cap (pre-authorizing/reserving the maximum possible cost before the
// upstream call) is intentionally out of scope for this patch: reservations
// leak whenever a request errors, is cancelled, trips the circuit breaker, or
// is rejected, which permanently pins a key at its cap. With no reservation
// there is no leak and no false rejection of concurrent same-key requests.
func (p *Plugin) checkBudget(pctx *plugin.Context, key string) error {
	if p.spendLimitUSD <= 0 {
		return nil // unlimited
	}
	// The store holds what PREVIOUS requests spent; it is written once, at the
	// after stage. Measurements carries what THIS request has spent so far,
	// which is zero everywhere except inside an agentic tool loop, where the
	// gateway re-runs this check per turn and stamps the running total.
	//
	// Without that term a per-turn check reads the same stored figure every
	// turn and stops nothing: a key at 99% of its cap got a whole loop, however
	// many turns and however large the context grew. The loop is the one place
	// a single request can spend without bound, so it is the one place the soft
	// cap has to be able to close mid-request.
	current := p.store.get(key)
	if pctx.Measurements.HasCost {
		current += pctx.Measurements.CostUSD
	}
	if current >= p.spendLimitUSD {
		pctx.Reject = true
		pctx.Reason = fmt.Sprintf("budget exceeded: spent $%.4f of $%.2f limit", current, p.spendLimitUSD)
		return nil
	}
	return nil
}

// rate reads an optional per-million rate; an unset one costs nothing on its
// own dimension.
func rate(perM *float64) float64 {
	if perM == nil {
		return 0
	}
	return *perM
}

// perM converts a per-million-token rate to the cost of n tokens.
func perM(ratePerM float64, n int) float64 {
	return ratePerM * float64(n) / 1_000_000.0
}

// recordCost calculates the actual USD cost from token usage and adds it to
// the store via a single atomic read-modify-write, so concurrent completions
// for the same key never lose an increment.
//
// The token classes are split exactly as models.Calculate splits them, against
// the operator's configured rates instead of the catalog's:
//
//   - PromptTokens is INCLUSIVE of CacheReadTokens, so when a cache-read rate is
//     configured the cached subset comes off the input-rate count — otherwise it
//     is paid for twice, once at the full input rate and again at the cache rate.
//     Clamped at zero: a provider reporting more cached than prompt tokens must
//     not produce a negative billable count.
//   - With no cache-read rate configured there is no discount to apply, so the
//     cached subset stays on the input rate rather than becoming free. An
//     unknown rate must not silently bill as zero.
//   - CacheWriteTokens sit outside PromptTokens and bill only on their own rate.
//   - ReasoningTokens are a subset of CompletionTokens and are already priced by
//     the output rate; adding them again would double-bill.
func (p *Plugin) recordCost(pctx *plugin.Context, key string) {
	usage, ok := usageFromContext(pctx)
	if !ok {
		return
	}
	promptBillable := usage.PromptTokens
	if p.cacheReadPerMTokens != nil {
		promptBillable = max(usage.PromptTokens-usage.CacheReadTokens, 0)
	}
	actual := perM(p.inputPerMTokens, promptBillable) +
		perM(p.outputPerMTokens, usage.CompletionTokens) +
		perM(rate(p.cacheReadPerMTokens), usage.CacheReadTokens) +
		perM(rate(p.cacheWritePerMTokens), usage.CacheWriteTokens)
	if actual > 0 {
		p.store.add(key, actual)
	}
}
