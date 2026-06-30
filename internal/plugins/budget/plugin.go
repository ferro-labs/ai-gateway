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
//     rejects the request with HTTP 429 if the committed spend is at or over
//     the limit. This is a read-only SOFT-cap check (no reservation).
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
//	store_id: "default"           # shared ID between before/after instances
//	spend_limit_usd: 50.0         # max cumulative spend per API key (USD)
//	input_per_m_tokens: 3.0       # cost per 1M prompt tokens (USD)
//	output_per_m_tokens: 15.0     # cost per 1M completion tokens (USD)
//	max_keys: 10000               # max tracked keys per store; evicts min-spend key at cap
//
// # Memory and retention
//
// All spend data is in-memory and does not survive process restarts. This
// makes the budget plugin suitable for session-scoped soft limits and
// development quotas. For durable billing enforcement use FerroCloud's
// server-side budget controls which persist to PostgreSQL.
//
// The store caps tracked keys at max_keys (default 10,000). When the cap is
// reached on a new key insertion, the key with the lowest accumulated spend is
// evicted to make room. Use [ResetStore] or [ResetStoreKey] for explicit
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

	"github.com/ferro-labs/ai-gateway/internal/plugins/plugincfg"
	"github.com/ferro-labs/ai-gateway/plugin"
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
	return v.(*spendStore) //nolint:forcetypeassert
}

// ResetStoreKey removes the accumulated spend for apiKey from the named store.
// This can be used after API key rotation or for operational housekeeping.
func ResetStoreKey(storeID, apiKey string) {
	v, ok := globalStores.Load(storeID)
	if !ok {
		return
	}
	v.(*spendStore).reset(apiKey) //nolint:forcetypeassert
}

// ResetStore clears all accumulated spend for every key in the named store.
func ResetStore(storeID string) {
	v, ok := globalStores.Load(storeID)
	if !ok {
		return
	}
	v.(*spendStore).resetAll() //nolint:forcetypeassert
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
	store            *spendStore
}

// Name returns the plugin identifier.
func (p *Plugin) Name() string { return "budget" }

// Type returns the plugin lifecycle hook type.
func (p *Plugin) Type() plugin.PluginType { return plugin.TypeRateLimit }

// Init reads the plugin configuration.
func (p *Plugin) Init(config map[string]any) error {
	p.storeID = "default"
	if v, ok := config["store_id"].(string); ok && v != "" {
		p.storeID = v
	}

	if v, ok := config["spend_limit_usd"]; ok {
		f, err := plugincfg.ToFloat64(v)
		if err != nil {
			return fmt.Errorf("budget: spend_limit_usd: %w", err)
		}
		if f < 0 {
			return fmt.Errorf("budget: spend_limit_usd must be >= 0")
		}
		p.spendLimitUSD = f
	}

	if v, ok := config["input_per_m_tokens"]; ok {
		f, err := plugincfg.ToFloat64(v)
		if err != nil {
			return fmt.Errorf("budget: input_per_m_tokens: %w", err)
		}
		p.inputPerMTokens = f
	}

	if v, ok := config["output_per_m_tokens"]; ok {
		f, err := plugincfg.ToFloat64(v)
		if err != nil {
			return fmt.Errorf("budget: output_per_m_tokens: %w", err)
		}
		p.outputPerMTokens = f
	}

	maxKeys := defaultMaxKeys
	if v, ok := config["max_keys"]; ok {
		n, err := plugincfg.ToFloat64(v)
		if err != nil {
			return fmt.Errorf("budget: max_keys: %w", err)
		}
		if n < 0 {
			return fmt.Errorf("budget: max_keys must be >= 0")
		}
		maxKeys = int(n)
	}

	if p.spendLimitUSD > 0 && p.inputPerMTokens == 0 && p.outputPerMTokens == 0 {
		return fmt.Errorf("budget: spend_limit_usd is set but both input_per_m_tokens and output_per_m_tokens are 0; cost will always be 0 and the budget limit will never be enforced")
	}

	p.store = getStore(p.storeID, maxKeys)
	return nil
}

// Execute checks or records spend depending on the pipeline stage.
//
// When pctx.Response is nil (before_request stage), it checks the accumulated
// spend for the API key and rejects the request if the limit is exceeded.
//
// When pctx.Response is non-nil (after_request stage), it calculates the cost
// of the completed request from token usage and adds it to the store.
func (p *Plugin) Execute(_ context.Context, pctx *plugin.Context) error {
	key, ok := pctx.Metadata["api_key"].(string)
	if !ok || key == "" {
		// No API key in context — skip per-key budget tracking.
		return nil
	}

	if pctx.Response == nil {
		// before_request stage: check accumulated spend.
		return p.checkBudget(pctx, key)
	}

	// after_request stage: record cost.
	p.recordCost(pctx, key)
	return nil
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
	current := p.store.get(key)
	if current >= p.spendLimitUSD {
		pctx.Reject = true
		pctx.Reason = fmt.Sprintf("budget exceeded: spent $%.4f of $%.2f limit", current, p.spendLimitUSD)
		return fmt.Errorf("budget exceeded for api key")
	}
	return nil
}

// recordCost calculates the actual USD cost from token usage and adds it to
// the store via a single atomic read-modify-write, so concurrent completions
// for the same key never lose an increment.
func (p *Plugin) recordCost(pctx *plugin.Context, key string) {
	if pctx.Response == nil {
		return
	}
	usage := pctx.Response.Usage
	actual := (float64(usage.PromptTokens)/1_000_000.0)*p.inputPerMTokens +
		(float64(usage.CompletionTokens)/1_000_000.0)*p.outputPerMTokens
	if actual > 0 {
		p.store.add(key, actual)
	}
}
