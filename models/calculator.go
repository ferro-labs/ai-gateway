package models

// Usage carries all token and media counts from a completed provider response.
// This is intentionally a separate type from providers.Usage so the models
// package has no dependency on the providers package and can be imported
// independently (e.g. by FerroCloud).
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	ReasoningTokens  int     // o1/o3 models — billed separately
	CacheReadTokens  int     // prompt cache hits (cheaper)
	CacheWriteTokens int     // prompt cache misses, written to cache
	ImageCount       int     // image generation requests
	AudioInputSecs   float64 // audio transcription (Whisper)
	AudioOutputChars int     // TTS (character count)
}

// CostResult breaks down the total cost by billing component.
// Every field is in USD.
type CostResult struct {
	TotalUSD      float64
	InputUSD      float64
	OutputUSD     float64
	CacheReadUSD  float64
	CacheWriteUSD float64
	ReasoningUSD  float64
	ImageUSD      float64
	AudioUSD      float64
	EmbeddingUSD  float64
	// ModelFound is false when the catalog has no entry for the requested model.
	// All cost fields will be zero in that case.
	ModelFound bool
	// Priced is true when the model was found and has at least one non-null price
	// for its primary billing dimension. A false Priced with a true ModelFound
	// means the catalog entry exists but pricing is unknown (e.g. credit-based
	// providers). Routing strategies should treat unpriced models as unknown cost,
	// not as $0.
	Priced bool
}

// perM converts a nullable price-per-million-tokens to a cost for n tokens.
// Returns 0 when price is nil (field not applicable) or n is 0.
func perM(price *float64, n int) float64 {
	if price == nil || n == 0 {
		return 0
	}
	return *price * float64(n) / 1_000_000
}

// Calculate computes the full cost for a completed request.
// modelKey should be "provider/model-id"; a bare model ID is also accepted
// and resolved via the reverse index built at catalog load time.
func Calculate(catalog Catalog, modelKey string, usage Usage) CostResult {
	model, ok := catalog.GetForPricing(modelKey)
	if !ok {
		return CostResult{ModelFound: false}
	}

	p := model.Pricing
	r := CostResult{ModelFound: true}

	r.Priced = p.InputPerMTokens != nil

	switch model.Mode {
	case ModeChat:
		r.InputUSD = perM(p.InputPerMTokens, usage.PromptTokens)
		r.OutputUSD = perM(p.OutputPerMTokens, usage.CompletionTokens)
		r.CacheReadUSD = perM(p.CacheReadPerMTokens, usage.CacheReadTokens)
		r.CacheWriteUSD = perM(p.CacheWritePerMTokens, usage.CacheWriteTokens)
		r.ReasoningUSD = perM(p.ReasoningPerMTokens, usage.ReasoningTokens)
		r.Priced = p.InputPerMTokens != nil

	case ModeEmbedding:
		r.EmbeddingUSD = perM(p.EmbeddingPerMTokens, usage.PromptTokens)
		r.Priced = p.EmbeddingPerMTokens != nil

	case ModeImage:
		if p.ImagePerTile != nil && usage.ImageCount > 0 {
			r.ImageUSD = *p.ImagePerTile * float64(usage.ImageCount)
		}
		r.Priced = p.ImagePerTile != nil

	case ModeAudioIn:
		if p.AudioInputPerMinute != nil && usage.AudioInputSecs > 0 {
			r.AudioUSD = *p.AudioInputPerMinute * usage.AudioInputSecs / 60
		}
		r.Priced = p.AudioInputPerMinute != nil

	case ModeAudioOut:
		if p.AudioOutputPerCharacter != nil && usage.AudioOutputChars > 0 {
			r.AudioUSD = *p.AudioOutputPerCharacter * float64(usage.AudioOutputChars)
		}
		r.Priced = p.AudioOutputPerCharacter != nil
	}

	r.TotalUSD = r.InputUSD + r.OutputUSD + r.CacheReadUSD +
		r.CacheWriteUSD + r.ReasoningUSD + r.ImageUSD + r.AudioUSD + r.EmbeddingUSD
	return r
}
