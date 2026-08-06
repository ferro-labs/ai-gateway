package models

// Usage carries all token and media counts from a completed provider response.
// This is intentionally a separate type from providers.Usage so the models
// package has no dependency on the providers package and can be imported
// independently.
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
	// Priced is false when the catalog entry carries no price for the field its
	// mode bills off: input tokens for chat, embedding tokens for embeddings,
	// per-tile or (failing that, and only against reported usage) per-token for
	// images, per-minute or per-character for audio.
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

	// Priced is per MODE, because each mode bills off a different price field.
	// Reading InputPerMTokens for all of them reported every embedding and every
	// image model unpriced however complete its catalog entry was — and
	// cost-optimized routing skips unpriced candidates, so under
	// `unpriced_strategy: skip` no embedding target could rank at all.
	switch model.Mode {
	case ModeChat:
		r.Priced = p.InputPerMTokens != nil
		// PromptTokens is INCLUSIVE of CacheReadTokens on every provider: that
		// is the OpenAI convention, and providers/core/anthropicwire folds
		// Anthropic's exclusive count into it at decode so exactly one
		// convention reaches here. So the cached subset must come off the
		// input-rate count, or it is paid for twice — once at the full input
		// rate inside PromptTokens and again at the cache rate below.
		//
		// With no cache rate in the catalog there is no discount to apply, so
		// the cached subset stays on the input rate rather than becoming free:
		// an unknown rate must not silently bill as zero.
		promptBillable := usage.PromptTokens
		if p.CacheReadPerMTokens != nil {
			// Clamped: a provider reporting more cached than prompt tokens
			// must not produce a negative billable count.
			promptBillable = max(usage.PromptTokens-usage.CacheReadTokens, 0)
		}
		r.InputUSD = perM(p.InputPerMTokens, promptBillable)
		r.OutputUSD = perM(p.OutputPerMTokens, usage.CompletionTokens)
		r.CacheReadUSD = perM(p.CacheReadPerMTokens, usage.CacheReadTokens)
		r.CacheWriteUSD = perM(p.CacheWritePerMTokens, usage.CacheWriteTokens)
		r.ReasoningUSD = perM(p.ReasoningPerMTokens, usage.ReasoningTokens)

	case ModeEmbedding:
		r.Priced = p.EmbeddingPerMTokens != nil
		r.EmbeddingUSD = perM(p.EmbeddingPerMTokens, usage.PromptTokens)

	case ModeImage:
		// Two billing shapes live under this one mode. Most image models carry a
		// per-tile price; the gpt-image family and fireworks' image models carry
		// none and are billed per token instead. Per-tile wins wherever both are
		// present — the gemini and vertex-ai image rows carry both — because
		// per-image is what those providers actually bill.
		//
		// The token arm requires reported usage, and that guard is the point of
		// it. Every image provider but OpenAI and Azure OpenAI reports none, and
		// pricing a zero count would record $0.00 as though it were the real
		// figure while flagging the request priced: the known-zero this
		// mode-aware flag exists to prevent. No usage means unpriced, which is
		// the true answer.
		switch {
		case p.ImagePerTile != nil:
			r.Priced = true
			if usage.ImageCount > 0 {
				r.ImageUSD = *p.ImagePerTile * float64(usage.ImageCount)
			}
		case usage.PromptTokens > 0 || usage.CompletionTokens > 0:
			r.Priced = p.InputPerMTokens != nil || p.OutputPerMTokens != nil
			r.InputUSD = perM(p.InputPerMTokens, usage.PromptTokens)
			r.OutputUSD = perM(p.OutputPerMTokens, usage.CompletionTokens)
		}

	case ModeAudioIn:
		r.Priced = p.AudioInputPerMinute != nil
		if p.AudioInputPerMinute != nil && usage.AudioInputSecs > 0 {
			r.AudioUSD = *p.AudioInputPerMinute * usage.AudioInputSecs / 60
		}

	case ModeAudioOut:
		r.Priced = p.AudioOutputPerCharacter != nil
		if p.AudioOutputPerCharacter != nil && usage.AudioOutputChars > 0 {
			r.AudioUSD = *p.AudioOutputPerCharacter * float64(usage.AudioOutputChars)
		}
	}

	r.TotalUSD = r.InputUSD + r.OutputUSD + r.CacheReadUSD +
		r.CacheWriteUSD + r.ReasoningUSD + r.ImageUSD + r.AudioUSD + r.EmbeddingUSD
	return r
}
