package providers

// ModelPricing holds per-token prices in USD per 1 million tokens.
type ModelPricing struct {
	InputPer1M  float64
	OutputPer1M float64
}

// PricingTable maps "provider/model" keys to pricing data.
// Prices are in USD per 1 million tokens (as listed on public pricing pages).
// This table is best-effort and may lag behind provider price changes.
var PricingTable = map[string]ModelPricing{
	// OpenAI
	"openai/gpt-4o":                 {InputPer1M: 2.50, OutputPer1M: 10.00},
	"openai/gpt-4o-mini":            {InputPer1M: 0.15, OutputPer1M: 0.60},
	"openai/gpt-4-turbo":            {InputPer1M: 10.00, OutputPer1M: 30.00},
	"openai/gpt-4":                  {InputPer1M: 30.00, OutputPer1M: 60.00},
	"openai/gpt-3.5-turbo":          {InputPer1M: 0.50, OutputPer1M: 1.50},
	"openai/text-embedding-3-small": {InputPer1M: 0.02, OutputPer1M: 0.00},
	"openai/text-embedding-3-large": {InputPer1M: 0.13, OutputPer1M: 0.00},
	"openai/text-embedding-ada-002": {InputPer1M: 0.10, OutputPer1M: 0.00},

	// Anthropic
	"anthropic/claude-3-5-sonnet-20241022": {InputPer1M: 3.00, OutputPer1M: 15.00},
	"anthropic/claude-3-5-haiku-20241022":  {InputPer1M: 0.80, OutputPer1M: 4.00},
	"anthropic/claude-3-opus-20240229":     {InputPer1M: 15.00, OutputPer1M: 75.00},
	"anthropic/claude-3-sonnet-20240229":   {InputPer1M: 3.00, OutputPer1M: 15.00},
	"anthropic/claude-3-haiku-20240307":    {InputPer1M: 0.25, OutputPer1M: 1.25},

	// Google Gemini
	"gemini/gemini-1.5-pro":   {InputPer1M: 1.25, OutputPer1M: 5.00},
	"gemini/gemini-1.5-flash": {InputPer1M: 0.075, OutputPer1M: 0.30},
	"gemini/gemini-1.0-pro":   {InputPer1M: 0.50, OutputPer1M: 1.50},

	// Groq (approximate — Groq pricing is token-based)
	"groq/llama-3.3-70b-versatile": {InputPer1M: 0.59, OutputPer1M: 0.79},
	"groq/llama-3.1-8b-instant":    {InputPer1M: 0.05, OutputPer1M: 0.08},
	"groq/mixtral-8x7b-32768":      {InputPer1M: 0.24, OutputPer1M: 0.24},

	// Mistral
	"mistral/mistral-large-latest":  {InputPer1M: 2.00, OutputPer1M: 6.00},
	"mistral/mistral-medium-latest": {InputPer1M: 2.70, OutputPer1M: 8.10},
	"mistral/mistral-small-latest":  {InputPer1M: 0.20, OutputPer1M: 0.60},
	"mistral/open-mistral-7b":       {InputPer1M: 0.25, OutputPer1M: 0.25},

	// Together AI
	"together/meta-llama/Llama-3-70b-chat-hf":       {InputPer1M: 0.90, OutputPer1M: 0.90},
	"together/meta-llama/Llama-3-8b-chat-hf":        {InputPer1M: 0.20, OutputPer1M: 0.20},
	"together/mistralai/Mixtral-8x7B-Instruct-v0.1": {InputPer1M: 0.60, OutputPer1M: 0.60},

	// Cohere
	"cohere/command-r-plus": {InputPer1M: 2.50, OutputPer1M: 10.00},
	"cohere/command-r":      {InputPer1M: 0.15, OutputPer1M: 0.60},
	"cohere/command":        {InputPer1M: 1.00, OutputPer1M: 2.00},

	// DeepSeek
	"deepseek/deepseek-chat":  {InputPer1M: 0.14, OutputPer1M: 0.28},
	"deepseek/deepseek-coder": {InputPer1M: 0.14, OutputPer1M: 0.28},

	// Perplexity
	"perplexity/sonar":               {InputPer1M: 1.00, OutputPer1M: 1.00},
	"perplexity/sonar-pro":           {InputPer1M: 3.00, OutputPer1M: 15.00},
	"perplexity/sonar-reasoning":     {InputPer1M: 1.00, OutputPer1M: 5.00},
	"perplexity/sonar-reasoning-pro": {InputPer1M: 2.00, OutputPer1M: 8.00},

	// Fireworks AI
	"fireworks/accounts/fireworks/models/llama-v3p1-8b-instruct":  {InputPer1M: 0.20, OutputPer1M: 0.20},
	"fireworks/accounts/fireworks/models/llama-v3p1-70b-instruct": {InputPer1M: 0.90, OutputPer1M: 0.90},
	"fireworks/accounts/fireworks/models/firefunction-v2":         {InputPer1M: 0.90, OutputPer1M: 0.90},

	// AI21
	"ai21/jamba-1.5-large": {InputPer1M: 2.00, OutputPer1M: 8.00},
	"ai21/jamba-1.5-mini":  {InputPer1M: 0.20, OutputPer1M: 0.40},
}

// EstimateCost returns the estimated cost in USD for a completed response.
// It looks up pricing by "provider/model" key and falls back to zero if
// the model is not in the pricing table.
func EstimateCost(provider, model string, usage Usage) float64 {
	key := provider + "/" + model
	p, ok := PricingTable[key]
	if !ok {
		return 0
	}
	inputCost := float64(usage.PromptTokens) / 1_000_000 * p.InputPer1M
	outputCost := float64(usage.CompletionTokens) / 1_000_000 * p.OutputPer1M
	return inputCost + outputCost
}
