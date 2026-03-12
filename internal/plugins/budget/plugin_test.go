package budget

import (
	"context"
	"testing"

	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
)

func makePlugin(t *testing.T, cfg map[string]interface{}) *Plugin {
	t.Helper()
	p := &Plugin{}
	if err := p.Init(cfg); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	return p
}

func pctxWithKey(key string) *plugin.Context {
	pctx := plugin.NewContext(&providers.Request{User: "u1"})
	pctx.Metadata["api_key"] = key
	return pctx
}

func TestBudget_Init_Defaults(t *testing.T) {
	p := makePlugin(t, map[string]interface{}{})
	if p.storeID != "default" {
		t.Errorf("default store_id should be 'default', got %q", p.storeID)
	}
	if p.spendLimitUSD != 0 {
		t.Errorf("default spend_limit_usd should be 0 (unlimited)")
	}
}

func TestBudget_Init_InvalidType(t *testing.T) {
	p := &Plugin{}
	err := p.Init(map[string]interface{}{"spend_limit_usd": "not-a-number"})
	if err == nil {
		t.Fatal("expected error for non-numeric spend_limit_usd")
	}
}

func TestBudget_Init_NegativeLimit(t *testing.T) {
	p := &Plugin{}
	err := p.Init(map[string]interface{}{"spend_limit_usd": -1.0})
	if err == nil {
		t.Fatal("expected error for negative spend_limit_usd")
	}
}

func TestBudget_Init_ZeroPricingWithLimit(t *testing.T) {
	// spend_limit_usd > 0 but both pricing rates are 0 → error at Init.
	p := &Plugin{}
	err := p.Init(map[string]interface{}{
		"spend_limit_usd": 10.0,
		// input_per_m_tokens and output_per_m_tokens default to 0
	})
	if err == nil {
		t.Fatal("expected error when spend_limit_usd > 0 but both pricing rates are 0")
	}
}

func TestBudget_NoAPIKey_Skips(t *testing.T) {
	// No api_key in metadata → plugin should not reject.
	p := makePlugin(t, map[string]interface{}{
		"spend_limit_usd":     0.01,
		"input_per_m_tokens":  1.0,
		"output_per_m_tokens": 1.0,
	})
	pctx := plugin.NewContext(&providers.Request{})
	if err := p.Execute(context.Background(), pctx); err != nil {
		t.Errorf("should skip when no api_key, got error: %v", err)
	}
	if pctx.Reject {
		t.Error("should not reject when no api_key set")
	}
}

func TestBudget_BelowLimit_Passes(t *testing.T) {
	// Use a unique store_id to avoid pollution from other tests.
	p := makePlugin(t, map[string]interface{}{
		"store_id":            "test-below",
		"spend_limit_usd":     10.0,
		"input_per_m_tokens":  3.0,
		"output_per_m_tokens": 15.0,
	})
	pctx := pctxWithKey("key-below")
	if err := p.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("should pass when spend is 0: %v", err)
	}
	if pctx.Reject {
		t.Error("should not reject when under limit")
	}
}

func TestBudget_RecordAndExceed(t *testing.T) {
	p := makePlugin(t, map[string]interface{}{
		"store_id":            "test-exceed",
		"spend_limit_usd":     0.001, // $0.001 limit
		"input_per_m_tokens":  3.0,
		"output_per_m_tokens": 15.0,
	})
	apiKey := "key-exceed"
	// Simulate after_request: record a response with 100 prompt + 50 completion tokens.
	// cost = (100/1_000_000 * 3.0) + (50/1_000_000 * 15.0)
	//
	//	= 0.0003 + 0.00075 = 0.00105 USD  → over the $0.001 limit
	afterPctx := pctxWithKey(apiKey)
	afterPctx.Response = &providers.Response{
		Usage: providers.Usage{
			PromptTokens:     100,
			CompletionTokens: 50,
		},
	}
	if err := p.Execute(context.Background(), afterPctx); err != nil {
		t.Fatalf("after_request recording should not error: %v", err)
	}
	// Now the before_request check should reject (spend > limit).
	beforePctx := pctxWithKey(apiKey)
	err := p.Execute(context.Background(), beforePctx)
	if err == nil {
		t.Fatal("expected rejection after exceeding spend limit")
	}
	if !beforePctx.Reject {
		t.Error("pctx.Reject should be set to true when budget exceeded")
	}
}

func TestBudget_Unlimited_NeverRejects(t *testing.T) {
	// spend_limit_usd = 0 means unlimited.
	p := makePlugin(t, map[string]interface{}{
		"store_id":            "test-unlimited",
		"input_per_m_tokens":  3.0,
		"output_per_m_tokens": 15.0,
	})
	apiKey := "key-unlimited"
	// Record a huge cost.
	afterPctx := pctxWithKey(apiKey)
	afterPctx.Response = &providers.Response{
		Usage: providers.Usage{
			PromptTokens:     1_000_000,
			CompletionTokens: 1_000_000,
		},
	}
	_ = p.Execute(context.Background(), afterPctx)
	// Before request should still pass because no limit configured.
	beforePctx := pctxWithKey(apiKey)
	if err := p.Execute(context.Background(), beforePctx); err != nil {
		t.Errorf("unlimited budget should never reject, got: %v", err)
	}
}

func TestBudget_SharedStore_TwoInstances(t *testing.T) {
	cfg := map[string]interface{}{
		"store_id":            "test-shared",
		"spend_limit_usd":     0.001,
		"input_per_m_tokens":  3.0,
		"output_per_m_tokens": 15.0,
	}
	recorder := makePlugin(t, cfg)
	checker := makePlugin(t, cfg)
	apiKey := "key-shared"
	// Record via one instance.
	afterPctx := pctxWithKey(apiKey)
	afterPctx.Response = &providers.Response{
		Usage: providers.Usage{PromptTokens: 100, CompletionTokens: 50},
	}
	_ = recorder.Execute(context.Background(), afterPctx)
	// Check via other instance — they share the same store.
	beforePctx := pctxWithKey(apiKey)
	err := checker.Execute(context.Background(), beforePctx)
	if err == nil {
		t.Fatal("shared store: checker should see spend recorded by recorder")
	}
}

func TestBudget_MaxKeys_EvictsMinSpend(t *testing.T) {
	// max_keys=2 means at most 2 keys tracked; adding a 3rd evicts the lowest-spend one.
	p := makePlugin(t, map[string]interface{}{
		"store_id":            "test-max-keys",
		"spend_limit_usd":     10.0,
		"input_per_m_tokens":  1.0,
		"output_per_m_tokens": 1.0,
		"max_keys":            2.0,
	})

	// Record spend for two keys (equal cost: 1 prompt token = $0.000001).
	recordCost := func(key string, tokens int) {
		pctx := pctxWithKey(key)
		pctx.Response = &providers.Response{
			Usage: providers.Usage{PromptTokens: tokens},
		}
		_ = p.Execute(context.Background(), pctx)
	}

	recordCost("low-spend", 1)   // $0.000001
	recordCost("high-spend", 10) // $0.00001 — stays, higher spend

	// Adding a third key must evict "low-spend" (min spend).
	recordCost("new-key", 5)

	store := getStore("test-max-keys", 2)
	if store.get("low-spend") != 0 {
		t.Error("low-spend key should have been evicted")
	}
	if store.get("high-spend") == 0 {
		t.Error("high-spend key should still be present")
	}
}

func TestBudget_ResetStoreKey(t *testing.T) {
	p := makePlugin(t, map[string]interface{}{
		"store_id":            "test-reset-key",
		"spend_limit_usd":     0.001,
		"input_per_m_tokens":  3.0,
		"output_per_m_tokens": 15.0,
	})
	apiKey := "key-to-reset"

	// Record spend that exceeds limit.
	afterPctx := pctxWithKey(apiKey)
	afterPctx.Response = &providers.Response{
		Usage: providers.Usage{PromptTokens: 100, CompletionTokens: 50},
	}
	_ = p.Execute(context.Background(), afterPctx)

	// Confirm over budget.
	if err := p.Execute(context.Background(), pctxWithKey(apiKey)); err == nil {
		t.Fatal("expected budget exceeded before reset")
	}

	// Reset the key and confirm budget is clear.
	ResetStoreKey("test-reset-key", apiKey)
	if err := p.Execute(context.Background(), pctxWithKey(apiKey)); err != nil {
		t.Errorf("after ResetStoreKey, request should pass: %v", err)
	}
}

func TestBudget_ResetStore(t *testing.T) {
	p := makePlugin(t, map[string]interface{}{
		"store_id":            "test-reset-all",
		"spend_limit_usd":     0.001,
		"input_per_m_tokens":  3.0,
		"output_per_m_tokens": 15.0,
	})

	for _, k := range []string{"key-a", "key-b"} {
		pctx := pctxWithKey(k)
		pctx.Response = &providers.Response{
			Usage: providers.Usage{PromptTokens: 100, CompletionTokens: 50},
		}
		_ = p.Execute(context.Background(), pctx)
	}

	ResetStore("test-reset-all")

	for _, k := range []string{"key-a", "key-b"} {
		if err := p.Execute(context.Background(), pctxWithKey(k)); err != nil {
			t.Errorf("after ResetStore, key %q should pass: %v", k, err)
		}
	}
}
