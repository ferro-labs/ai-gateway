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

	// PromptTokens is inclusive of CacheReadTokens, so 1.5M prompt tokens of
	// which 500k were cache hits leaves 1M billing at the input rate.
	got := Calculate(c, "anthropic/claude-3-7-sonnet", Usage{
		PromptTokens:     1_500_000,
		CompletionTokens: 1_000_000,
		CacheReadTokens:  500_000,
		CacheWriteTokens: 1_000_000,
		ReasoningTokens:  1_000_000,
	})

	if got.InputUSD != 3.0 {
		t.Errorf("InputUSD: got %v, want 3.0", got.InputUSD)
	}
	if got.OutputUSD != 15.0 {
		t.Errorf("OutputUSD: got %v, want 15.0", got.OutputUSD)
	}
	if got.CacheReadUSD != 0.15 {
		t.Errorf("CacheReadUSD: got %v, want 0.15", got.CacheReadUSD)
	}
	if got.CacheWriteUSD != 3.75 {
		t.Errorf("CacheWriteUSD: got %v, want 3.75", got.CacheWriteUSD)
	}
	if got.ReasoningUSD != 15.0 {
		t.Errorf("ReasoningUSD: got %v, want 15.0", got.ReasoningUSD)
	}
	want := 3.0 + 15.0 + 0.15 + 3.75 + 15.0
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

// ---- Priced is per mode --------------------------------------------------

// Priced must answer "does the catalog price what this mode bills off", not
// "does it have an input-token price".
//
// It was resolved from InputPerMTokens before the mode switch ran, and no mode
// except chat bills off that field — so every embedding model and every image
// model reported unpriced however complete its catalog entry was. The value is
// load-bearing: cost-optimized routing ranks only priced candidates, so under
// `unpriced_strategy: skip` an embeddings request could not rank a single
// target and was refused outright.
func TestCalculatePricedFollowsTheModesOwnPriceField(t *testing.T) {
	tests := []struct {
		name       string
		mode       ModelMode
		pricing    Pricing
		usage      Usage
		wantPriced bool
		wantTotal  float64
	}{
		{
			name:       "embedding with an embedding price",
			mode:       ModeEmbedding,
			pricing:    Pricing{EmbeddingPerMTokens: ptr(0.02)},
			usage:      Usage{PromptTokens: 1_000_000},
			wantPriced: true,
			wantTotal:  0.02,
		},
		{
			name:    "embedding with no embedding price",
			mode:    ModeEmbedding,
			pricing: Pricing{},
			usage:   Usage{PromptTokens: 1_000_000},
		},
		{
			// An input-token price is not an embedding price and buys nothing
			// here — reporting priced would promise a cost this arm cannot bill.
			name:    "embedding priced only on input tokens",
			mode:    ModeEmbedding,
			pricing: Pricing{InputPerMTokens: ptr(3.0)},
			usage:   Usage{PromptTokens: 1_000_000},
		},
		{
			name:       "image with a per-tile price",
			mode:       ModeImage,
			pricing:    Pricing{ImagePerTile: ptr(0.04)},
			usage:      Usage{ImageCount: 2},
			wantPriced: true,
			wantTotal:  0.08,
		},
		{
			name:    "image with no per-tile price",
			mode:    ModeImage,
			pricing: Pricing{},
			usage:   Usage{ImageCount: 2},
		},
		{
			// The gpt-image shape: no per-tile price, a per-token one, and a
			// provider that reports the tokens. This is a real bill and must be
			// reported as one.
			name:       "image priced per token, usage reported",
			mode:       ModeImage,
			pricing:    Pricing{InputPerMTokens: ptr(5.0), OutputPerMTokens: ptr(10.0)},
			usage:      Usage{ImageCount: 1, PromptTokens: 1_000_000, CompletionTokens: 1_000_000},
			wantPriced: true,
			wantTotal:  15.0,
		},
		{
			// The same catalog row against a provider that reports no usage —
			// every image provider except OpenAI and Azure OpenAI. There is
			// nothing to bill, and claiming $0.00 was billed is the known-zero
			// this flag exists to prevent.
			name:    "image priced per token, no usage reported",
			mode:    ModeImage,
			pricing: Pricing{InputPerMTokens: ptr(5.0), OutputPerMTokens: ptr(10.0)},
			usage:   Usage{ImageCount: 1},
		},
		{
			// The gemini and vertex-ai image rows carry both prices. Per-image is
			// what those providers bill, so the token fallback must not reach
			// them.
			name:       "image with both a per-tile and a per-token price bills per tile",
			mode:       ModeImage,
			pricing:    Pricing{ImagePerTile: ptr(0.04), InputPerMTokens: ptr(5.0), OutputPerMTokens: ptr(10.0)},
			usage:      Usage{ImageCount: 2, PromptTokens: 1_000_000, CompletionTokens: 1_000_000},
			wantPriced: true,
			wantTotal:  0.08,
		},
		{
			// dall-e-2/-3 and the nscale image rows: the catalog prices them no
			// way at all. Reported usage must not conjure a price.
			name:    "image the catalog prices no way stays unpriced despite usage",
			mode:    ModeImage,
			pricing: Pricing{},
			usage:   Usage{ImageCount: 1, PromptTokens: 1_000_000, CompletionTokens: 1_000_000},
		},
		{
			name:       "audio in with a per-minute price",
			mode:       ModeAudioIn,
			pricing:    Pricing{AudioInputPerMinute: ptr(0.006)},
			usage:      Usage{AudioInputSecs: 60},
			wantPriced: true,
			wantTotal:  0.006,
		},
		{
			name:    "audio in with no per-minute price",
			mode:    ModeAudioIn,
			pricing: Pricing{},
			usage:   Usage{AudioInputSecs: 60},
		},
		{
			name:       "audio out with a per-character price",
			mode:       ModeAudioOut,
			pricing:    Pricing{AudioOutputPerCharacter: ptr(0.00003)},
			usage:      Usage{AudioOutputChars: 100},
			wantPriced: true,
			wantTotal:  0.003,
		},
		{
			name:    "audio out with no per-character price",
			mode:    ModeAudioOut,
			pricing: Pricing{},
			usage:   Usage{AudioOutputChars: 100},
		},
		{
			name:       "chat is unchanged: input price decides",
			mode:       ModeChat,
			pricing:    Pricing{InputPerMTokens: ptr(3.0)},
			usage:      Usage{PromptTokens: 1_000_000},
			wantPriced: true,
			wantTotal:  3.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := catalogWith("p/m", Model{Provider: "p", ModelID: "m", Mode: tt.mode, Pricing: tt.pricing})

			got := Calculate(c, "p/m", tt.usage)

			if !got.ModelFound {
				t.Fatal("ModelFound should be true; the rest of this case proves nothing otherwise")
			}
			if got.Priced != tt.wantPriced {
				t.Errorf("Priced = %v, want %v — cost-optimized routing skips unpriced candidates",
					got.Priced, tt.wantPriced)
			}
			if math.Abs(got.TotalUSD-tt.wantTotal) > 1e-12 {
				t.Errorf("TotalUSD = %v, want %v", got.TotalUSD, tt.wantTotal)
			}
		})
	}
}

// ---- Cache-read is billed once, not twice --------------------------------

// PromptTokens is inclusive of CacheReadTokens on every provider (the OpenAI
// convention; providers/core/anthropicwire folds Anthropic's exclusive count
// into it at decode). Pricing the FULL prompt at the input rate and then adding
// the cache-read cost on top charged the cached subset twice — once at the full
// input rate and once at the cache rate.
//
// The worked case is the one from the report: gpt-4o, 10000 prompt tokens of
// which 8000 were cache hits, $2.50/M input and $1.25/M cache read.
//
//	double-billed: 10000*2.50/M + 8000*1.25/M = $0.025 + $0.010 = $0.035
//	correct:        2000*2.50/M + 8000*1.25/M = $0.005 + $0.010 = $0.015
func TestCalculateChatCachedPromptBillsInputOnRemainderOnly(t *testing.T) {
	c := catalogWith("openai/gpt-4o", Model{
		Provider: "openai",
		ModelID:  "gpt-4o",
		Mode:     ModeChat,
		Pricing: Pricing{
			InputPerMTokens:     ptr(2.50),
			CacheReadPerMTokens: ptr(1.25),
		},
	})

	got := Calculate(c, "openai/gpt-4o", Usage{PromptTokens: 10_000, CacheReadTokens: 8_000})

	if math.Abs(got.InputUSD-0.005) > 1e-12 {
		t.Errorf("InputUSD = %v, want 0.005 (2000 uncached tokens, not all 10000)", got.InputUSD)
	}
	if math.Abs(got.CacheReadUSD-0.010) > 1e-12 {
		t.Errorf("CacheReadUSD = %v, want 0.010", got.CacheReadUSD)
	}
	if math.Abs(got.TotalUSD-0.015) > 1e-12 {
		t.Errorf("TotalUSD = %v, want 0.015 (0.035 is the double-billed figure)", got.TotalUSD)
	}
}

// The two wire conventions must reach the same bill. Anthropic reports 2000
// fresh + 8000 cache-read; the anthropicwire decoder folds that into
// PromptTokens: 10000, which is byte-for-byte the usage an OpenAI-wire provider
// reports for the same workload. Same input, same cost.
func TestCalculateChatCachedCostIsWireConventionIndependent(t *testing.T) {
	c := catalogWith("p/m", Model{
		Provider: "p", ModelID: "m", Mode: ModeChat,
		Pricing: Pricing{InputPerMTokens: ptr(2.50), CacheReadPerMTokens: ptr(1.25)},
	})

	// What core.Usage looks like after either provider's decode.
	openAIWire := Usage{PromptTokens: 10_000, CacheReadTokens: 8_000}
	anthropicWire := Usage{PromptTokens: 2_000 + 8_000, CacheReadTokens: 8_000}

	a := Calculate(c, "p/m", openAIWire)
	b := Calculate(c, "p/m", anthropicWire)
	if a.TotalUSD != b.TotalUSD {
		t.Fatalf("openai-wire $%v != anthropic-wire $%v for the same workload", a.TotalUSD, b.TotalUSD)
	}
	if math.Abs(a.TotalUSD-0.015) > 1e-12 {
		t.Errorf("TotalUSD = %v, want 0.015", a.TotalUSD)
	}
}

// A request with no cache hits must price exactly as it always did: the whole
// prompt at the input rate.
func TestCalculateChatUncachedPromptIsUnchanged(t *testing.T) {
	c := catalogWith("openai/gpt-4o", Model{
		Provider: "openai", ModelID: "gpt-4o", Mode: ModeChat,
		Pricing: Pricing{InputPerMTokens: ptr(2.50), CacheReadPerMTokens: ptr(1.25)},
	})

	got := Calculate(c, "openai/gpt-4o", Usage{PromptTokens: 10_000})

	if math.Abs(got.InputUSD-0.025) > 1e-12 {
		t.Errorf("InputUSD = %v, want 0.025 (all 10000 tokens billable)", got.InputUSD)
	}
	if got.CacheReadUSD != 0 {
		t.Errorf("CacheReadUSD = %v, want 0", got.CacheReadUSD)
	}
}

// With no cache rate in the catalog there is no discount to apply, so the
// cached subset stays on the input rate rather than silently becoming free.
func TestCalculateChatCachedPromptWithoutCacheRateBillsAtInputRate(t *testing.T) {
	c := catalogWith("p/m", Model{
		Provider: "p", ModelID: "m", Mode: ModeChat,
		Pricing: Pricing{InputPerMTokens: ptr(2.50)}, // no CacheReadPerMTokens
	})

	got := Calculate(c, "p/m", Usage{PromptTokens: 10_000, CacheReadTokens: 8_000})

	if math.Abs(got.InputUSD-0.025) > 1e-12 {
		t.Errorf("InputUSD = %v, want 0.025 — an unknown cache rate must not bill as zero", got.InputUSD)
	}
	if math.Abs(got.TotalUSD-0.025) > 1e-12 {
		t.Errorf("TotalUSD = %v, want 0.025", got.TotalUSD)
	}
}

// A provider reporting more cached tokens than prompt tokens is nonsense, but
// it must not produce a negative billable count (and so a negative cost).
func TestCalculateChatCacheReadExceedingPromptClampsAtZero(t *testing.T) {
	c := catalogWith("p/m", Model{
		Provider: "p", ModelID: "m", Mode: ModeChat,
		Pricing: Pricing{InputPerMTokens: ptr(2.50), CacheReadPerMTokens: ptr(1.25)},
	})

	got := Calculate(c, "p/m", Usage{PromptTokens: 1_000, CacheReadTokens: 8_000})

	if got.InputUSD != 0 {
		t.Errorf("InputUSD = %v, want 0 (clamped, never negative)", got.InputUSD)
	}
	if got.TotalUSD < 0 {
		t.Errorf("TotalUSD = %v, must never be negative", got.TotalUSD)
	}
}
