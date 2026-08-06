// This file carries NO build tag, on purpose.
//
// Everything else in test/live sits behind `-tags=live`, so nothing here ran in
// CI and the pinned model ids rotted unnoticed. This file runs under plain
// `go test ./...` and fails the moment a pin leaves the catalog, is deprecated,
// or is used on the wrong surface — so a red live run is unambiguous: the
// product broke, not the fixture aged out.

package live_test

import (
	"testing"

	"github.com/ferro-labs/ai-gateway/models"
)

// liveModel is one model id the live suite calls, with its catalog surface.
type liveModel struct {
	provider string
	id       string
	mode     models.ModelMode
}

// key is the catalog key for the pin: "provider/model-id".
func (m liveModel) key() string { return m.provider + "/" + m.id }

// groqChatModel is groq's pinned chat model. It is a named const because the
// groq gateway and discovery live tests reference it by more than one path, and
// the pin below must stay the single source for all of them.
const groqChatModel = "openai/gpt-oss-20b"

// groqChat is kept as a standalone pin because groq_gateway_test.go and
// groq_discovery_test.go reference it directly; livePins["groq"] mirrors it and
// TestLiveModelsAreCurrent asserts they never diverge.
var groqChat = liveModel{"groq", groqChatModel, models.ModeChat}

// livePin is a provider's pinned model per surface. An empty field means the
// provider is env-only on that surface: its live file reads the model from
// LIVE_<PROVIDER>_<SURFACE>_MODEL and skips when unset. That is the honest
// spelling for deployment/config/special-auth providers (Azure deployments,
// local Ollama pulls, Replicate declared models, Bedrock/Vertex credentials)
// whose model id a public catalog cannot pin.
type livePin struct {
	chat  string
	embed string
	image string
}

// livePins is the catalog-backed pin table — the single source the drift guard
// checks and the per-provider live files resolve their default model from.
// Every non-empty id here is a current (non-deprecated) catalog model on the
// stated surface; TestLiveModelsAreCurrent enforces that offline in CI.
//
// The seven providers deliberately absent (azure-foundry, azure-openai,
// cloudflare, hugging-face, ollama, replicate, vertex-ai) are env-only: their
// routable model is a deployment/config/account-specific id no catalog can
// know, so pinning one would be a fiction. Their live files still exist and run
// the moment LIVE_<PROVIDER>_<SURFACE>_MODEL and the credential are supplied.
var livePins = map[string]livePin{
	"ai21":         {chat: "jamba-1.5"},
	"anthropic":    {chat: "claude-haiku-4-5"},
	"bedrock":      {chat: "amazon.nova-micro-v1:0", embed: "amazon.titan-embed-text-v2:0", image: "amazon.nova-canvas-v1:0"},
	"cerebras":     {chat: "gpt-oss-120b"},
	"cohere":       {chat: "command-r7b-12-2024", embed: "embed-v4.0"},
	"databricks":   {chat: "databricks-meta-llama-3-3-70b-instruct", embed: "databricks-gte-large-en"},
	"deepinfra":    {chat: "meta-llama/Meta-Llama-3.1-8B-Instruct"},
	"deepseek":     {chat: "deepseek-chat"},
	"fireworks":    {chat: "accounts/fireworks/models/llama-v3p1-8b-instruct", image: "accounts/fireworks/models/flux-1-schnell-fp8"},
	"gemini":       {chat: "gemini-2.5-flash-lite", embed: "gemini-embedding-001", image: "gemini-2.5-flash-image"},
	"groq":         {chat: groqChatModel},
	"mistral":      {chat: "codestral-2405", embed: "mistral-embed"},
	"moonshot":     {chat: "moonshot-v1-8k"},
	"novita":       {chat: "deepseek/deepseek-v3.1", embed: "baai/bge-m3"},
	"nvidia-nim":   {chat: "meta/llama-3.1-8b-instruct"},
	"ollama-cloud": {chat: "gpt-oss:20b"},
	"openai":       {chat: "gpt-4.1-nano", embed: "text-embedding-3-small", image: "gpt-image-1-mini"},
	"openrouter":   {chat: "openrouter/auto"},
	"perplexity":   {chat: "sonar"},
	"qwen":         {chat: "qwen-turbo"},
	"sambanova":    {chat: "Meta-Llama-3.3-70B-Instruct"},
	"together":     {chat: "openai/gpt-oss-20b"},
	"xai":          {chat: "grok-4-1-fast"},
}

// TestLiveModelsAreCurrent fails when a pin has left the catalog, been
// deprecated, or is used on the wrong surface. It reads the EMBEDDED catalog
// snapshot — no network — so it answers the same on a laptop, in CI and in an
// air-gapped build, and it runs under plain `go test` rather than the opt-in
// live suite, which is the whole point: an aged-out pin is caught here, before a
// live run mistakes it for a product defect.
func TestLiveModelsAreCurrent(t *testing.T) {
	t.Setenv(models.CatalogFetchTimeoutEnv, "0")
	catalog, err := models.Load()
	if err != nil {
		t.Fatalf("load embedded model catalog: %v", err)
	}

	if got := livePins["groq"].chat; got != groqChat.id {
		t.Fatalf("groqChat.id = %q but livePins[groq].chat = %q; keep them in sync", groqChat.id, got)
	}

	surfaces := []struct {
		name  string
		model func(livePin) string
		mode  models.ModelMode
	}{
		{"chat", func(p livePin) string { return p.chat }, models.ModeChat},
		{"embed", func(p livePin) string { return p.embed }, models.ModeEmbedding},
		{"image", func(p livePin) string { return p.image }, models.ModeImage},
	}

	for provider, pin := range livePins {
		for _, s := range surfaces {
			id := s.model(pin)
			if id == "" {
				continue
			}
			lm := liveModel{provider, id, s.mode}
			t.Run(lm.key(), func(t *testing.T) {
				m, ok := catalog.Get(lm.key())
				if !ok {
					t.Fatalf("%s is not in the model catalog; a live run against it can only fail", lm.key())
				}
				if m.IsDeprecated() {
					t.Errorf("%s is %s in the catalog (deprecation_date=%s); re-pin the live suite to a current model",
						lm.key(), m.Lifecycle.Status, deref(m.Lifecycle.DeprecationDate))
				}
				if m.Mode != lm.mode {
					t.Errorf("%s is a %q model in the catalog but the live suite calls it as %q",
						lm.key(), m.Mode, lm.mode)
				}
			})
		}
	}
}

func deref(s *string) string {
	if s == nil {
		return "none"
	}
	return *s
}
