// Package models provides the model catalog — a structured map of every
// supported model's pricing, capabilities, and lifecycle metadata.
//
// The catalog is loaded once at gateway startup from a remote URL with an
// embedded backup as fallback. Cost calculation via [Calculate] is performed
// synchronously after the upstream provider responds, before the gateway
// publishes its completion event.
package models

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

//go:embed catalog_backup.json
var bundledCatalog []byte

// CatalogURLEnv is the env var operators set to override the catalog source.
// Useful for air-gapped deployments or enterprise custom pricing.
const CatalogURLEnv = "FERRO_MODEL_CATALOG_URL"

const defaultCatalogURL = "https://raw.githubusercontent.com/ferro-labs/ai-gateway/main/models/catalog.json"

// Catalog is a flat map of "provider/model-id" → Model.
type Catalog map[string]Model

// Model holds all metadata for a single model.
type Model struct {
	Provider        string       `json:"provider"`
	ModelID         string       `json:"model_id"`
	DisplayName     string       `json:"display_name"`
	Mode            ModelMode    `json:"mode"`
	ContextWindow   int          `json:"context_window"`
	MaxOutputTokens int          `json:"max_output_tokens"`
	Pricing         Pricing      `json:"pricing"`
	Capabilities    Capabilities `json:"capabilities"`
	Lifecycle       Lifecycle    `json:"lifecycle"`
	Source          string       `json:"source"`
	UpdatedAt       string       `json:"updated_at"`
}

// ModelMode identifies what kind of requests a model handles.
type ModelMode string

// Model mode constants used in catalog entries and cost calculation dispatch.
const (
	ModeChat      ModelMode = "chat"
	ModeEmbedding ModelMode = "embedding"
	ModeImage     ModelMode = "image"
	ModeAudioIn   ModelMode = "audio_in"
	ModeAudioOut  ModelMode = "audio_out"
)

// Pricing holds all cost fields in USD.
// Token prices are per 1M tokens. nil means the field is not applicable to
// this model's mode — it does NOT mean free. Use 0 for genuinely free models.
type Pricing struct {
	InputPerMTokens          *float64 `json:"input_per_m_tokens"`
	OutputPerMTokens         *float64 `json:"output_per_m_tokens"`
	CacheReadPerMTokens      *float64 `json:"cache_read_per_m_tokens"`
	CacheWritePerMTokens     *float64 `json:"cache_write_per_m_tokens"`
	ReasoningPerMTokens      *float64 `json:"reasoning_per_m_tokens"`
	ImagePerTile             *float64 `json:"image_per_tile"`
	AudioInputPerMinute      *float64 `json:"audio_input_per_minute"`
	AudioOutputPerCharacter  *float64 `json:"audio_output_per_character"`
	EmbeddingPerMTokens      *float64 `json:"embedding_per_m_tokens"`
	FinetuneTrainPerMTokens  *float64 `json:"finetune_train_per_m_tokens"`
	FinetuneInputPerMTokens  *float64 `json:"finetune_input_per_m_tokens"`
	FinetuneOutputPerMTokens *float64 `json:"finetune_output_per_m_tokens"`
}

// Capabilities describes what features a model supports.
type Capabilities struct {
	Vision            bool `json:"vision"`
	AudioInput        bool `json:"audio_input"`
	AudioOutput       bool `json:"audio_output"`
	FunctionCalling   bool `json:"function_calling"`
	ParallelToolCalls bool `json:"parallel_tool_calls"`
	JSONMode          bool `json:"json_mode"`
	ResponseSchema    bool `json:"response_schema"`
	PromptCaching     bool `json:"prompt_caching"`
	Reasoning         bool `json:"reasoning"`
	Streaming         bool `json:"streaming"`
	Finetuneable      bool `json:"finetuneable"`
}

// Lifecycle describes a model's release and deprecation state.
type Lifecycle struct {
	Status          string  `json:"status"` // preview | ga | deprecated
	DeprecationDate *string `json:"deprecation_date"`
	SunsetDate      *string `json:"sunset_date"`
	Successor       *string `json:"successor"`
}

// Load fetches the model catalog from a remote URL (1s timeout).
// On any failure it falls back to the embedded catalog_backup.json.
// The gateway never fails to start due to catalog unavailability.
func Load() (Catalog, error) {
	url := os.Getenv(CatalogURLEnv)
	if url == "" {
		url = defaultCatalogURL
	}

	if data, err := fetchRemote(url); err == nil {
		if c, err := parse(data); err == nil {
			return c, nil
		}
		// Remote payload parsed successfully but was invalid JSON — fall through.
	}
	// Silent fallback — use the embedded copy shipped with the binary.
	return parse(bundledCatalog)
}

func fetchRemote(url string) ([]byte, error) {
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("catalog fetch: HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func parse(data []byte) (Catalog, error) {
	var c Catalog
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("catalog parse: %w", err)
	}
	return c, nil
}

// Get looks up a model by "provider/model-id".
// If not found, scans for a bare model ID match as fallback.
func (c Catalog) Get(key string) (Model, bool) {
	if m, ok := c[key]; ok {
		return m, true
	}
	// Bare model ID: return the first matching entry.
	for _, v := range c {
		if v.ModelID == key {
			return v, true
		}
	}
	return Model{}, false
}

// IsDeprecated returns true when the model's lifecycle status is deprecated or legacy.
func (m Model) IsDeprecated() bool {
	return m.Lifecycle.Status == "deprecated" || m.Lifecycle.Status == "legacy"
}
