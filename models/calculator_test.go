package models

import (
	"math"
	"testing"
)

// approxEqual returns true if a and b differ by less than epsilon.
func approxEqual(a, b, epsilon float64) bool {
	return math.Abs(a-b) < epsilon
}

// ptr returns a pointer to the given float64 — helper for test fixtures.
func ptr(f float64) *float64 { return &f }

// catalogWith builds a single-entry Catalog for use in calculator tests.
func catalogWith(key string, m Model) Catalog {
	return Catalog{key: m}
}

// ---- Chat mode -----------------------------------------------------------

func TestCalculateChatBasic(t *testing.T) {
	c := catalogWith("openai/gpt-4o", Model{
		Provider: "openai",
		ModelID:  "gpt-4o",
		Mode:     ModeChat,
		Pricing: Pricing{
			InputPerMTokens:  ptr(5.0),  // $5 / 1M
			OutputPerMTokens: ptr(15.0), // $15 / 1M
		},
	})

	got := Calculate(c, "openai/gpt-4o", Usage{
		PromptTokens:     1_000_000,
		CompletionTokens: 500_000,
	})

	if !got.ModelFound {
		t.Fatal("ModelFound should be true")
	}
	if got.InputUSD != 5.0 {
		t.Errorf("InputUSD: got %v, want 5.0", got.InputUSD)
	}
	if got.OutputUSD != 7.5 {
		t.Errorf("OutputUSD: got %v, want 7.5", got.OutputUSD)
	}
	if got.TotalUSD != 12.5 {
		t.Errorf("TotalUSD: got %v, want 12.5", got.TotalUSD)
	}
	if !got.Priced {
		t.Error("Priced should be true when token pricing is present")
	}
}

func TestCalculateChatCacheAndReasoning(t *testing.T) {
	c := catalogWith("anthropic/claude-3-7-sonnet", Model{
		Provider: "anthropic",
		ModelID:  "claude-3-7-sonnet",
		Mode:     ModeChat,
		Pricing: Pricing{
			InputPerMTokens:      ptr(3.0),
			OutputPerMTokens:     ptr(15.0),
			CacheReadPerMTokens:  ptr(0.30),
			CacheWritePerMTokens: ptr(3.75),
			ReasoningPerMTokens:  ptr(15.0),
		},
	})

	got := Calculate(c, "anthropic/claude-3-7-sonnet", Usage{
		PromptTokens:     1_000_000,
		CompletionTokens: 1_000_000,
		CacheReadTokens:  1_000_000,
		CacheWriteTokens: 1_000_000,
		ReasoningTokens:  1_000_000,
	})

	if got.InputUSD != 3.0 {
		t.Errorf("InputUSD: got %v, want 3.0", got.InputUSD)
	}
	if got.OutputUSD != 15.0 {
		t.Errorf("OutputUSD: got %v, want 15.0", got.OutputUSD)
	}
	if got.CacheReadUSD != 0.30 {
		t.Errorf("CacheReadUSD: got %v, want 0.30", got.CacheReadUSD)
	}
	if got.CacheWriteUSD != 3.75 {
		t.Errorf("CacheWriteUSD: got %v, want 3.75", got.CacheWriteUSD)
	}
	if got.ReasoningUSD != 15.0 {
		t.Errorf("ReasoningUSD: got %v, want 15.0", got.ReasoningUSD)
	}
	want := 3.0 + 15.0 + 0.30 + 3.75 + 15.0
	if got.TotalUSD != want {
		t.Errorf("TotalUSD: got %v, want %v", got.TotalUSD, want)
	}
}

// Nil pricing fields must return 0, not panic.
func TestCalculateChatNilPricing(t *testing.T) {
	c := catalogWith("openai/gpt-4o", Model{
		Provider: "openai",
		ModelID:  "gpt-4o",
		Mode:     ModeChat,
		Pricing:  Pricing{}, // all nil
	})

	got := Calculate(c, "openai/gpt-4o", Usage{
		PromptTokens:     100_000,
		CompletionTokens: 100_000,
		CacheReadTokens:  50_000,
		ReasoningTokens:  20_000,
	})

	if got.TotalUSD != 0 {
		t.Errorf("TotalUSD: got %v, want 0 for all-nil pricing", got.TotalUSD)
	}
	if got.Priced {
		t.Error("Priced should be false for all-nil pricing")
	}
}

func TestCalculateChatOutputOnlyPricingIsNotInputPriced(t *testing.T) {
	c := catalogWith("openai/output-only", Model{
		Provider: "openai",
		ModelID:  "output-only",
		Mode:     ModeChat,
		Pricing: Pricing{
			OutputPerMTokens: ptr(15.0),
		},
	})

	got := Calculate(c, "openai/output-only", Usage{
		PromptTokens:     100_000,
		CompletionTokens: 100_000,
	})

	if got.InputUSD != 0 {
		t.Errorf("InputUSD: got %v, want 0 when input pricing is nil", got.InputUSD)
	}
	if got.OutputUSD != 1.5 {
		t.Errorf("OutputUSD: got %v, want 1.5", got.OutputUSD)
	}
	if got.Priced {
		t.Error("Priced should be false when input pricing is nil")
	}
}

func TestCalculateChatZeroInputPriceIsPriced(t *testing.T) {
	c := catalogWith("openai/free-input", Model{
		Provider: "openai",
		ModelID:  "free-input",
		Mode:     ModeChat,
		Pricing: Pricing{
			InputPerMTokens: ptr(0),
		},
	})

	got := Calculate(c, "openai/free-input", Usage{PromptTokens: 100_000})

	if got.InputUSD != 0 {
		t.Errorf("InputUSD: got %v, want 0 for explicit free input pricing", got.InputUSD)
	}
	if !got.Priced {
		t.Error("Priced should be true when input pricing is explicitly zero")
	}
}

// Zero-valued usage should always produce $0 regardless of pricing rates.
func TestCalculateChatZeroUsage(t *testing.T) {
	c := catalogWith("openai/gpt-4o", Model{
		Provider: "openai",
		ModelID:  "gpt-4o",
		Mode:     ModeChat,
		Pricing: Pricing{
			InputPerMTokens:  ptr(5.0),
			OutputPerMTokens: ptr(15.0),
		},
	})

	got := Calculate(c, "openai/gpt-4o", Usage{})
	if got.TotalUSD != 0 {
		t.Errorf("TotalUSD: got %v, want 0 for zero usage", got.TotalUSD)
	}
}

// ---- Embedding mode ------------------------------------------------------

func TestCalculateEmbedding(t *testing.T) {
	c := catalogWith("openai/text-embedding-3-small", Model{
		Provider: "openai",
		ModelID:  "text-embedding-3-small",
		Mode:     ModeEmbedding,
		Pricing: Pricing{
			EmbeddingPerMTokens: ptr(0.02),
		},
	})

	got := Calculate(c, "openai/text-embedding-3-small", Usage{PromptTokens: 1_000_000})

	if got.EmbeddingUSD != 0.02 {
		t.Errorf("EmbeddingUSD: got %v, want 0.02", got.EmbeddingUSD)
	}
	if got.TotalUSD != got.EmbeddingUSD {
		t.Errorf("TotalUSD should equal EmbeddingUSD for embedding mode")
	}
}

// ---- Image mode ----------------------------------------------------------

func TestCalculateImage(t *testing.T) {
	c := catalogWith("openai/dall-e-3", Model{
		Provider: "openai",
		ModelID:  "dall-e-3",
		Mode:     ModeImage,
		Pricing: Pricing{
			ImagePerTile: ptr(0.04),
		},
	})

	got := Calculate(c, "openai/dall-e-3", Usage{ImageCount: 3})

	if got.ImageUSD != 0.12 {
		t.Errorf("ImageUSD: got %v, want 0.12", got.ImageUSD)
	}
	if got.TotalUSD != got.ImageUSD {
		t.Errorf("TotalUSD should equal ImageUSD for image mode")
	}
}

func TestCalculateImageZeroCount(t *testing.T) {
	c := catalogWith("openai/dall-e-3", Model{
		Provider: "openai",
		ModelID:  "dall-e-3",
		Mode:     ModeImage,
		Pricing:  Pricing{ImagePerTile: ptr(0.04)},
	})

	got := Calculate(c, "openai/dall-e-3", Usage{ImageCount: 0})
	if got.ImageUSD != 0 {
		t.Errorf("ImageUSD: got %v, want 0 for zero ImageCount", got.ImageUSD)
	}
}

// ---- Audio modes ---------------------------------------------------------

func TestCalculateAudioIn(t *testing.T) {
	c := catalogWith("openai/whisper-1", Model{
		Provider: "openai",
		ModelID:  "whisper-1",
		Mode:     ModeAudioIn,
		Pricing: Pricing{
			AudioInputPerMinute: ptr(0.006), // $0.006/min
		},
	})

	got := Calculate(c, "openai/whisper-1", Usage{AudioInputSecs: 120}) // 2 minutes

	if got.AudioUSD != 0.012 {
		t.Errorf("AudioUSD: got %v, want 0.012", got.AudioUSD)
	}
}

func TestCalculateAudioOut(t *testing.T) {
	c := catalogWith("openai/tts-1", Model{
		Provider: "openai",
		ModelID:  "tts-1",
		Mode:     ModeAudioOut,
		Pricing: Pricing{
			AudioOutputPerCharacter: ptr(0.000015),
		},
	})

	got := Calculate(c, "openai/tts-1", Usage{AudioOutputChars: 1000})

	if !approxEqual(got.AudioUSD, 0.015, 1e-9) {
		t.Errorf("AudioUSD: got %v, want 0.015", got.AudioUSD)
	}
}

// ---- Model not found -----------------------------------------------------

func TestCalculateModelNotFound(t *testing.T) {
	got := Calculate(Catalog{}, "openai/nonexistent", Usage{PromptTokens: 100})

	if got.ModelFound {
		t.Error("ModelFound should be false for unknown model")
	}
	if got.TotalUSD != 0 {
		t.Errorf("TotalUSD: got %v, want 0 for unknown model", got.TotalUSD)
	}
	if got.Priced {
		t.Error("Priced should be false for unknown model")
	}
}

func TestCalculateProviderAlias(t *testing.T) {
	c := catalogWith("azure/gpt-4o", Model{
		Provider: "azure",
		ModelID:  "gpt-4o",
		Mode:     ModeChat,
		Pricing: Pricing{
			InputPerMTokens:  ptr(2.5),
			OutputPerMTokens: ptr(10.0),
		},
	})

	got := Calculate(c, "azure-openai/gpt-4o", Usage{PromptTokens: 1_000_000})
	if !got.ModelFound {
		t.Fatal("ModelFound should be true for azure-openai alias")
	}
	if !got.Priced {
		t.Fatal("Priced should be true")
	}
	if got.InputUSD != 2.5 {
		t.Errorf("InputUSD: got %v, want 2.5", got.InputUSD)
	}
}

func TestCalculateProviderAliasAzureFoundryNoCacheRead(t *testing.T) {
	c := catalogWith("azure/gpt-4o", Model{
		Provider: "azure",
		ModelID:  "gpt-4o",
		Mode:     ModeChat,
		Pricing: Pricing{
			InputPerMTokens:     ptr(2.5),
			CacheReadPerMTokens: ptr(1.25),
		},
	})
	c["azure_foundry/gpt-4o"] = Model{
		Provider: "azure_foundry",
		ModelID:  "gpt-4o",
		Mode:     ModeChat,
		Pricing: Pricing{
			InputPerMTokens: ptr(2.5),
		},
	}

	got := Calculate(c, "azure-foundry/gpt-4o", Usage{
		PromptTokens:    1_000_000,
		CacheReadTokens: 500_000,
	})
	if !got.ModelFound || !got.Priced {
		t.Fatalf("ModelFound=%v Priced=%v", got.ModelFound, got.Priced)
	}
	if got.CacheReadUSD != 0 {
		t.Errorf("CacheReadUSD: got %v, want 0 when using azure_foundry entry", got.CacheReadUSD)
	}
}

func TestCalculateProviderAliasAzureOpenAIPrefersOpenAIPricing(t *testing.T) {
	c := catalogWith("azure_openai/gpt-4o-mini", Model{
		Provider: "azure_openai",
		ModelID:  "gpt-4o-mini",
		Mode:     ModeChat,
		Pricing: Pricing{
			InputPerMTokens:  ptr(0.15),
			OutputPerMTokens: ptr(0.60),
		},
	})
	c["azure/gpt-4o-mini"] = Model{
		Provider: "azure",
		ModelID:  "gpt-4o-mini",
		Mode:     ModeChat,
		Pricing: Pricing{
			InputPerMTokens:  ptr(0.165),
			OutputPerMTokens: ptr(0.66),
		},
	}

	got := Calculate(c, "azure-openai/gpt-4o-mini", Usage{PromptTokens: 1_000_000})
	if !got.ModelFound || !got.Priced {
		t.Fatalf("ModelFound=%v Priced=%v", got.ModelFound, got.Priced)
	}
	if got.InputUSD != 0.15 {
		t.Errorf("InputUSD: got %v, want 0.15 from azure_openai entry", got.InputUSD)
	}
}

func TestCalculateCatalogNativeKeyAzureFoundryPhi4(t *testing.T) {
	c, err := parse(bundledCatalog)
	if err != nil {
		t.Fatalf("parse bundled catalog: %v", err)
	}

	got := Calculate(c, "azure_foundry/phi-4", Usage{PromptTokens: 1_000_000})
	if !got.ModelFound {
		t.Fatal("ModelFound should be true for catalog-native azure_foundry/phi-4")
	}
	if !got.Priced {
		t.Fatal("Priced should be true via azure/Phi-4 fallback")
	}
	if got.InputUSD != 0.125 {
		t.Errorf("InputUSD: got %v, want 0.125", got.InputUSD)
	}
}

func TestCalculateProviderAliasCaseInsensitive(t *testing.T) {
	c := catalogWith("azure/Phi-4", Model{
		Provider: "azure",
		ModelID:  "Phi-4",
		Mode:     ModeChat,
		Pricing: Pricing{
			InputPerMTokens:  ptr(0.125),
			OutputPerMTokens: ptr(0.5),
		},
	})

	got := Calculate(c, "azure-foundry/phi-4", Usage{PromptTokens: 1_000_000})
	if !got.ModelFound {
		t.Fatal("ModelFound should be true for azure-foundry/phi-4 alias")
	}
	if !got.Priced {
		t.Fatal("Priced should be true")
	}
	if got.InputUSD != 0.125 {
		t.Errorf("InputUSD: got %v, want 0.125", got.InputUSD)
	}
}

// Bare model ID (no provider prefix) should resolve via reverse index.
func TestCalculateBareModelID(t *testing.T) {
	c := catalogWith("openai/gpt-4o-mini", Model{
		Provider: "openai",
		ModelID:  "gpt-4o-mini",
		Mode:     ModeChat,
		Pricing: Pricing{
			InputPerMTokens:  ptr(0.15),
			OutputPerMTokens: ptr(0.60),
		},
	})
	BuildIndex(c)

	got := Calculate(c, "gpt-4o-mini", Usage{PromptTokens: 1_000_000})
	if !got.ModelFound {
		t.Fatal("ModelFound should be true for bare model ID lookup")
	}
	if got.InputUSD != 0.15 {
		t.Errorf("InputUSD: got %v, want 0.15", got.InputUSD)
	}
}

// TotalUSD must always equal the sum of all component fields.
func TestCalculateTotalIsSumOfComponents(t *testing.T) {
	c := catalogWith("openai/gpt-4o", Model{
		Provider: "openai",
		ModelID:  "gpt-4o",
		Mode:     ModeChat,
		Pricing: Pricing{
			InputPerMTokens:      ptr(5.0),
			OutputPerMTokens:     ptr(15.0),
			CacheReadPerMTokens:  ptr(0.50),
			CacheWritePerMTokens: ptr(2.50),
			ReasoningPerMTokens:  ptr(10.0),
		},
	})

	got := Calculate(c, "openai/gpt-4o", Usage{
		PromptTokens:     500_000,
		CompletionTokens: 200_000,
		CacheReadTokens:  300_000,
		CacheWriteTokens: 100_000,
		ReasoningTokens:  50_000,
	})

	wantTotal := got.InputUSD + got.OutputUSD + got.CacheReadUSD +
		got.CacheWriteUSD + got.ReasoningUSD + got.ImageUSD +
		got.AudioUSD + got.EmbeddingUSD
	if got.TotalUSD != wantTotal {
		t.Errorf("TotalUSD %v != sum of components %v", got.TotalUSD, wantTotal)
	}
}
