package main

import (
	"github.com/ferro-labs/ai-gateway/models"
)

// EnrichedModelInfo extends the minimal OpenAI ModelInfo schema with catalog
// metadata. The extra fields are omitempty so the response stays
// backward-compatible for clients that only read id/object/owned_by.
type EnrichedModelInfo struct {
	ID              string   `json:"id"`
	Object          string   `json:"object"` // always "model"
	OwnedBy         string   `json:"owned_by"`
	Mode            string   `json:"mode,omitempty"`
	ContextWindow   int      `json:"context_window,omitempty"`
	MaxOutputTokens int      `json:"max_output_tokens,omitempty"`
	Capabilities    []string `json:"capabilities,omitempty"`
	Status          string   `json:"status,omitempty"`
	Deprecated      bool     `json:"deprecated,omitempty"`
}

// enrichFromCatalog builds an EnrichedModelInfo for provider/modelID by
// looking up the model catalog. Returns a minimal struct if no entry is found.
func enrichFromCatalog(catalog models.Catalog, provider, modelID string) EnrichedModelInfo {
	base := EnrichedModelInfo{
		ID:      modelID,
		Object:  "model",
		OwnedBy: provider,
	}

	m, ok := catalog.Get(provider + "/" + modelID)
	if !ok {
		return base
	}

	base.Mode = string(m.Mode)
	base.ContextWindow = m.ContextWindow
	base.MaxOutputTokens = m.MaxOutputTokens
	base.Capabilities = buildCapsList(m.Capabilities)
	base.Status = m.Lifecycle.Status
	base.Deprecated = m.IsDeprecated()
	return base
}

// buildCapsList converts the Capabilities struct to a string slice so the
// JSON response lists capabilities without requiring the client to know the
// full struct schema.
func buildCapsList(c models.Capabilities) []string {
	var caps []string
	if c.Vision {
		caps = append(caps, "vision")
	}
	if c.FunctionCalling {
		caps = append(caps, "function_calling")
	}
	if c.ParallelToolCalls {
		caps = append(caps, "parallel_tool_calls")
	}
	if c.JSONMode {
		caps = append(caps, "json_mode")
	}
	if c.ResponseSchema {
		caps = append(caps, "response_schema")
	}
	if c.Streaming {
		caps = append(caps, "streaming")
	}
	if c.PromptCaching {
		caps = append(caps, "prompt_caching")
	}
	if c.Reasoning {
		caps = append(caps, "reasoning")
	}
	if c.AudioInput {
		caps = append(caps, "audio_input")
	}
	if c.AudioOutput {
		caps = append(caps, "audio_output")
	}
	if c.Finetuneable {
		caps = append(caps, "finetuneable")
	}
	return caps
}
