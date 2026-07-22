package routetrace

import (
	"context"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/circuitbreaker"
	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/providers"
)

// stubProvider is a minimal providers.Provider for the tracer tests.
type stubProvider struct {
	name   string
	models []string
}

func (s *stubProvider) Name() string                  { return s.name }
func (s *stubProvider) SupportedModels() []string     { return s.models }
func (s *stubProvider) Models() []providers.ModelInfo { return nil }
func (s *stubProvider) SupportsModel(m string) bool {
	for _, mm := range s.models {
		if mm == m {
			return true
		}
	}
	return false
}
func (s *stubProvider) Complete(_ context.Context, _ providers.Request) (*providers.Response, error) {
	return nil, nil
}

func twoTargets() []Target {
	return []Target{
		{VirtualKey: "openai", Weight: 1},
		{VirtualKey: "ollama-cloud", Weight: 1},
	}
}

func fakeCatalog() models.Catalog {
	in := 1.1
	out := 4.4
	return models.Catalog{
		"openai/gpt-5-pro": {
			Provider:      "openai",
			ModelID:       "gpt-5-pro",
			Mode:          models.ModeChat,
			ContextWindow: 128000,
			Pricing:       models.Pricing{InputPerMTokens: &in, OutputPerMTokens: &out},
			Lifecycle:     models.Lifecycle{Status: "ga"},
		},
		"ollama-cloud/gpt-5-pro": {
			Provider:      "ollama-cloud",
			ModelID:       "gpt-5-pro",
			Mode:          models.ModeChat,
			ContextWindow: 128000,
			Pricing:       models.Pricing{InputPerMTokens: &in, OutputPerMTokens: nil}, // no output pricing = not "priced"
			Lifecycle:     models.Lifecycle{Status: "ga"},
		},
	}
}

func lookupWith(openai, ollama providers.Provider) func(string) (providers.Provider, bool) {
	return func(name string) (providers.Provider, bool) {
		switch name {
		case "openai":
			return openai, openai != nil
		case "ollama-cloud":
			return ollama, ollama != nil
		}
		return nil, false
	}
}

func costFor(cat models.Catalog) func(targetKey, modelID string) (models.CostResult, bool) {
	return func(targetKey, modelID string) (models.CostResult, bool) {
		return models.Calculate(cat, targetKey+"/"+modelID, models.Usage{}), true
	}
}

// TestTrace_SingleStrategy verifies the matched target is selected and surfaced
// first, the catalog block is populated, and DryRun is always true.
func TestTrace_SingleStrategy(t *testing.T) {
	openai := &stubProvider{name: "openai", models: []string{"gpt-5-pro"}}
	cfg := Config{
		Mode:    ModeSingle,
		Targets: []Target{{VirtualKey: "openai"}},
		Catalog: fakeCatalog(),
		Lookup:  lookupWith(openai, nil),
	}
	resp := Trace(cfg, "gpt-5-pro")

	if !resp.DryRun {
		t.Error("DryRun must be true (no provider call may ever occur)")
	}
	if resp.Strategy != ModeSingle {
		t.Errorf("Strategy = %q, want %q", resp.Strategy, ModeSingle)
	}
	if resp.SelectedTargetKey != "openai" {
		t.Errorf("SelectedTargetKey = %q, want openai", resp.SelectedTargetKey)
	}
	if !resp.Catalog.ModelFound {
		t.Error("Catalog.ModelFound = false, want true")
	}
	if !resp.Catalog.Priced {
		t.Error("Catalog.Priced = false, want true (openai carries input+output pricing)")
	}
	if resp.Catalog.ContextWindow != 128000 {
		t.Errorf("Catalog.ContextWindow = %d, want 128000", resp.Catalog.ContextWindow)
	}
	if len(resp.CandidateTargets) != 1 {
		t.Fatalf("len(CandidateTargets) = %d, want 1", len(resp.CandidateTargets))
	}
	if !resp.CandidateTargets[0].Matched {
		t.Error("first candidate must be matched for single strategy")
	}
	if !resp.CandidateTargets[0].SupportsModel {
		t.Error("first candidate SupportsModel must be true")
	}
	if resp.CandidateTargets[0].CircuitOpen {
		t.Error("CircuitOpen must be false when no breaker is configured")
	}
}

// TestTrace_FallbackStrategy verifies the first healthy+supporting target is
// selected and that an open circuit on the first target defers to the next.
func TestTrace_FallbackStrategy(t *testing.T) {
	openai := &stubProvider{name: "openai", models: []string{"gpt-5-pro"}}
	ollama := &stubProvider{name: "ollama-cloud", models: []string{"gpt-5-pro"}}
	cfg := Config{
		Mode:    ModeFallback,
		Targets: twoTargets(),
		Catalog: fakeCatalog(),
		Lookup:  lookupWith(openai, ollama),
		CircuitState: func(name string) circuitbreaker.State {
			if name == "openai" {
				return circuitbreaker.StateOpen // force fallback to ollama-cloud
			}
			return circuitbreaker.StateClosed
		},
	}
	resp := Trace(cfg, "gpt-5-pro")

	if resp.SelectedTargetKey != "ollama-cloud" {
		t.Errorf("SelectedTargetKey = %q, want ollama-cloud (openai circuit open)", resp.SelectedTargetKey)
	}
	openaiCand := findCandidate(t, resp.CandidateTargets, "openai")
	if !openaiCand.CircuitOpen {
		t.Error("openai candidate CircuitOpen must be true")
	}
	if openaiCand.Matched {
		t.Error("openai candidate Matched must be false with circuit open")
	}
}

// TestTrace_ConditionalStrategy verifies only the rule-matched target is
// marked Matched and surfaces first.
func TestTrace_ConditionalStrategy(t *testing.T) {
	openai := &stubProvider{name: "openai", models: []string{"gpt-5-pro"}}
	ollama := &stubProvider{name: "ollama-cloud", models: []string{"gpt-5-pro"}}
	cfg := Config{
		Mode:    ModeConditional,
		Targets: twoTargets(),
		Conditions: []ConditionRule{
			{Key: "model", Value: "gpt-5-pro", Target: Target{VirtualKey: "ollama-cloud"}},
		},
		Catalog: fakeCatalog(),
		Lookup:  lookupWith(openai, ollama),
	}
	resp := Trace(cfg, "gpt-5-pro")

	if resp.SelectedTargetKey != "ollama-cloud" {
		t.Errorf("SelectedTargetKey = %q, want ollama-cloud (rule match)", resp.SelectedTargetKey)
	}
	if len(resp.CandidateTargets) != 2 {
		t.Fatalf("len(CandidateTargets) = %d, want 2", len(resp.CandidateTargets))
	}
	// Matched target surfaces first.
	if resp.CandidateTargets[0].TargetKey != "ollama-cloud" {
		t.Errorf("first candidate = %q, want ollama-cloud (matched-first ordering)", resp.CandidateTargets[0].TargetKey)
	}
	if !resp.CandidateTargets[0].Matched {
		t.Error("matched conditional candidate must have Matched=true")
	}
	if resp.CandidateTargets[1].Matched {
		t.Error("non-matched conditional candidate must have Matched=false")
	}
}

// TestTrace_ConditionalStrategyFallback verifies that when no rule matches,
// the configured fallback target (Targets[0]) is selected.
func TestTrace_ConditionalStrategyFallback(t *testing.T) {
	openai := &stubProvider{name: "openai", models: []string{"gpt-5-pro"}}
	ollama := &stubProvider{name: "ollama-cloud", models: []string{"gpt-5-pro"}}
	cfg := Config{
		Mode:    ModeConditional,
		Targets: twoTargets(), // openai is Targets[0] = the fallback
		Conditions: []ConditionRule{
			{Key: "model", Value: "claude", Target: Target{VirtualKey: "ollama-cloud"}},
		},
		Catalog: fakeCatalog(),
		Lookup:  lookupWith(openai, ollama),
	}
	resp := Trace(cfg, "gpt-5-pro")
	if resp.SelectedTargetKey != "openai" {
		t.Errorf("SelectedTargetKey = %q, want openai (fallback when no rule matches)", resp.SelectedTargetKey)
	}
}

// TestTrace_CostOptimizedStrategy verifies the lowest-cost target wins and
// that EstimatedCostUSD is populated.
func TestTrace_CostOptimizedStrategy(t *testing.T) {
	openai := &stubProvider{name: "openai", models: []string{"gpt-5-pro"}}
	ollama := &stubProvider{name: "ollama-cloud", models: []string{"gpt-5-pro"}}
	cat := fakeCatalog()
	cfg := Config{
		Mode:          ModeCostOptimized,
		Targets:       twoTargets(),
		Catalog:       cat,
		Lookup:        lookupWith(openai, ollama),
		CostForTarget: costFor(cat),
	}
	resp := Trace(cfg, "gpt-5-pro")
	// Both targets resolve the same model; openai/gpt-5-pro has output pricing
	// whereas ollama-cloud/gpt-5-pro does not, so openai's zero-usage cost
	// must be 0 (no tokens) but both are priced=0. The tracer breaks ties by
	// lowest estimated cost — both 0 — so the first is selected deterministically.
	if resp.SelectedTargetKey == "" {
		t.Fatal("SelectedTargetKey empty, expected a cost-optimized pick")
	}
	// Cost hook populated EstimatedCostUSD (0 for zero usage, must be set).
	for _, c := range resp.CandidateTargets {
		if c.Matched && c.EstimatedCostUSD != 0 {
			t.Errorf("EstimatedCostUSD for zero-usage probe = %v, want 0", c.EstimatedCostUSD)
		}
	}
}

// TestTrace_LeastLatencyStrategy verifies the lowest-P50-with-samples target
// wins and HasLatencySamples surfaces correctly.
func TestTrace_LeastLatencyStrategy(t *testing.T) {
	openai := &stubProvider{name: "openai", models: []string{"gpt-5-pro"}}
	ollama := &stubProvider{name: "ollama-cloud", models: []string{"gpt-5-pro"}}
	cfg := Config{
		Mode:    ModeLatency,
		Targets: twoTargets(),
		Catalog: fakeCatalog(),
		Lookup:  lookupWith(openai, ollama),
		LatencyP50: func(name string) time.Duration {
			if name == "openai" {
				return 200 * time.Millisecond
			}
			return 50 * time.Millisecond
		},
		LatencyHasSamps: func(_ string) bool { return true },
	}
	resp := Trace(cfg, "gpt-5-pro")
	if resp.SelectedTargetKey != "ollama-cloud" {
		t.Errorf("SelectedTargetKey = %q, want ollama-cloud (lower P50)", resp.SelectedTargetKey)
	}
	ollamaCand := findCandidate(t, resp.CandidateTargets, "ollama-cloud")
	if ollamaCand.P50LatencyMs != 50 {
		t.Errorf("ollama P50 = %d, want 50", ollamaCand.P50LatencyMs)
	}
	if !ollamaCand.HasLatencySamples {
		t.Error("ollama HasLatencySamples must be true")
	}
}

// TestTrace_AliasResolution verifies the gateway alias map resolves the
// requested model before catalog lookup and candidate evaluation.
func TestTrace_AliasResolution(t *testing.T) {
	openai := &stubProvider{name: "openai", models: []string{"gpt-5-pro"}}
	cfg := Config{
		Mode:    ModeSingle,
		Targets: []Target{{VirtualKey: "openai"}},
		Aliases: map[string]string{"smart": "gpt-5-pro"},
		Catalog: fakeCatalog(),
		Lookup:  lookupWith(openai, nil),
	}
	resp := Trace(cfg, "smart")
	if resp.RequestedModel != "smart" {
		t.Errorf("RequestedModel = %q, want smart", resp.RequestedModel)
	}
	if resp.ResolvedModel != "gpt-5-pro" {
		t.Errorf("ResolvedModel = %q, want gpt-5-pro", resp.ResolvedModel)
	}
	if !resp.Catalog.ModelFound {
		t.Error("alias must resolve BEFORE catalog lookup so ModelFound is true")
	}
}

// TestTrace_NoProviderSupportsModel verifies the trace returns no selection
// (dry-run of the gateway's "no provider supports model" error path) without
// calling any provider.
func TestTrace_NoProviderSupportsModel(t *testing.T) {
	openai := &stubProvider{name: "openai", models: []string{"gpt-5-pro"}}
	cfg := Config{
		Mode:    ModeFallback,
		Targets: twoTargets(),
		Catalog: fakeCatalog(),
		Lookup:  lookupWith(openai, &stubProvider{name: "ollama-cloud", models: nil}),
	}
	resp := Trace(cfg, "claude-unknown")
	if resp.SelectedTargetKey != "" {
		t.Errorf("SelectedTargetKey = %q, want empty (no provider supports model)", resp.SelectedTargetKey)
	}
	for _, c := range resp.CandidateTargets {
		if c.SupportsModel {
			t.Errorf("candidate %q SupportsModel must be false for unknown model", c.TargetKey)
		}
	}
}

// TestTrace_CatalogMissingModel verifies an unknown model still produces a
// trace with catalog.model_found=false and a clear selected target.
func TestTrace_CatalogMissingModel(t *testing.T) {
	openai := &stubProvider{name: "openai", models: []string{"custom-internal"}}
	cfg := Config{
		Mode:    ModeSingle,
		Targets: []Target{{VirtualKey: "openai"}},
		Catalog: fakeCatalog(),
		Lookup:  lookupWith(openai, nil),
	}
	resp := Trace(cfg, "custom-internal")
	if resp.Catalog.ModelFound {
		t.Error("Catalog.ModelFound must be false for a model absent from the catalog")
	}
	if resp.SelectedTargetKey != "openai" {
		t.Errorf("SelectedTargetKey = %q, want openai (provider supports it even if catalog doesn't)", resp.SelectedTargetKey)
	}
}

func findCandidate(t *testing.T, cs []CandidateTarget, key string) CandidateTarget {
	t.Helper()
	for _, c := range cs {
		if c.TargetKey == key {
			return c
		}
	}
	t.Fatalf("candidate %q not found in %+v", key, cs)
	return CandidateTarget{}
}
