package providers

import (
	"math"
	"testing"
)

func TestEstimateCost_KnownModel(t *testing.T) {
	usage := Usage{
		PromptTokens:     1000,
		CompletionTokens: 500,
		TotalTokens:      1500,
	}
	cost := EstimateCost("openai", "gpt-4o", usage)
	// 1000/1M * 2.50 + 500/1M * 10.00 = 0.0025 + 0.005 = 0.0075
	expected := 0.0025 + 0.005
	if math.Abs(cost-expected) > 1e-10 {
		t.Errorf("EstimateCost() = %v, want %v", cost, expected)
	}
}

func TestEstimateCost_UnknownModel(t *testing.T) {
	usage := Usage{PromptTokens: 1000, CompletionTokens: 500}
	cost := EstimateCost("unknown", "unknown-model", usage)
	if cost != 0 {
		t.Errorf("EstimateCost() for unknown model = %v, want 0", cost)
	}
}

func TestEstimateCost_ZeroUsage(t *testing.T) {
	usage := Usage{}
	cost := EstimateCost("openai", "gpt-4o", usage)
	if cost != 0 {
		t.Errorf("EstimateCost() for zero usage = %v, want 0", cost)
	}
}

func TestEstimateCost_EmbeddingModel(t *testing.T) {
	usage := Usage{
		PromptTokens: 1_000_000, // 1M tokens
		TotalTokens:  1_000_000,
	}
	cost := EstimateCost("openai", "text-embedding-3-small", usage)
	// 1M/1M * 0.02 = 0.02
	expected := 0.02
	if math.Abs(cost-expected) > 1e-10 {
		t.Errorf("EstimateCost() for embedding = %v, want %v", cost, expected)
	}
}

func TestEstimateCost_AnthropicModel(t *testing.T) {
	usage := Usage{
		PromptTokens:     100,
		CompletionTokens: 50,
	}
	cost := EstimateCost("anthropic", "claude-3-5-sonnet-20241022", usage)
	// 100/1M * 3.00 + 50/1M * 15.00 = 0.0003 + 0.00075 = 0.00105
	expected := float64(100)/1_000_000*3.00 + float64(50)/1_000_000*15.00
	if math.Abs(cost-expected) > 1e-10 {
		t.Errorf("EstimateCost() = %v, want %v", cost, expected)
	}
}

func TestPricingTable_AllModelsHavePositivePrices(t *testing.T) {
	for key, pricing := range PricingTable {
		if pricing.InputPer1M < 0 {
			t.Errorf("PricingTable[%q].InputPer1M = %v, must be >= 0", key, pricing.InputPer1M)
		}
		if pricing.OutputPer1M < 0 {
			t.Errorf("PricingTable[%q].OutputPer1M = %v, must be >= 0", key, pricing.OutputPer1M)
		}
	}
}
