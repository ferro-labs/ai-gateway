package handler

import (
	"encoding/json"
	"net/http"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// EnrichedModelInfo extends the minimal OpenAI ModelInfo schema with catalog
// metadata. The extra fields are omitempty so the response stays
// backward-compatible for clients that only read id/object/owned_by.
type EnrichedModelInfo struct {
	ID     string `json:"id"`
	Object string `json:"object"` // always "model"
	// Created is the model's creation time as the provider reports it, in Unix
	// seconds. It is part of the OpenAI Model object and typed as a plain
	// integer by the official SDKs, so it is always written, never omitted: a
	// missing key fails a strict client's deserialization of the whole list.
	// Zero means the provider did not report one — only live model discovery
	// carries it, and the gateway does not invent a date for the rest.
	Created         int64    `json:"created"`
	OwnedBy         string   `json:"owned_by"`
	Mode            string   `json:"mode,omitempty"`
	ContextWindow   int      `json:"context_window,omitempty"`
	MaxOutputTokens int      `json:"max_output_tokens,omitempty"`
	Capabilities    []string `json:"capabilities,omitempty"`
	Status          string   `json:"status,omitempty"`
	Deprecated      bool     `json:"deprecated,omitempty"`
}

// enrichFromCatalog builds an EnrichedModelInfo for one model by looking up the
// model catalog. Returns the plain OpenAI fields if no entry is found.
func enrichFromCatalog(catalog models.Catalog, info core.ModelInfo) EnrichedModelInfo {
	base := EnrichedModelInfo{
		ID:      info.ID,
		Object:  "model",
		Created: info.Created,
		OwnedBy: info.OwnedBy,
	}

	m, ok := catalog.Get(info.OwnedBy + "/" + info.ID)
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

// Models handles GET /v1/models.
func Models(gw *aigateway.Gateway) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		catalog := gw.Catalog()
		raw := gw.AllModels()
		enriched := make([]EnrichedModelInfo, 0, len(raw))
		for _, m := range raw {
			enriched = append(enriched, enrichFromCatalog(catalog, m))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data":   enriched,
		})
	}
}
