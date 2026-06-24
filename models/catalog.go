// Package models provides the model catalog — a structured map of every
// supported model's pricing, capabilities, and lifecycle metadata.
//
// The catalog is loaded from a remote URL with an embedded backup as fallback.
// Cost calculation via [Calculate] is performed synchronously after the upstream
// provider responds, before the gateway publishes its completion event.
package models

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

//go:embed catalog_backup.json
var bundledCatalog []byte

// CatalogURLEnv is the env var operators set to override the catalog source.
// Useful for air-gapped deployments or enterprise custom pricing.
const CatalogURLEnv = "FERRO_MODEL_CATALOG_URL"

const defaultCatalogURL = "https://github.com/ferro-labs/model-catalog/releases/latest/download/catalog.json"

// LoadSource identifies where a catalog load came from.
type LoadSource string

const (
	// LoadSourceRemote means the catalog was loaded from the configured URL.
	LoadSourceRemote LoadSource = "remote"
	// LoadSourceFallback means the embedded backup catalog was used.
	LoadSourceFallback LoadSource = "fallback"
)

// LoadResult describes a completed catalog load.
type LoadResult struct {
	Catalog Catalog
	Source  LoadSource
	URL     string
}

// modelIDIndex is a reverse lookup from bare model ID to catalog key. It is
// rebuilt by parse() on every catalog load so loaded catalogs can resolve bare
// model IDs in O(1) instead of scanning all ~2,500 entries.
//
// Catalog is intentionally still a map type for API compatibility, so Get()
// validates indexed hits against the receiver and falls back to scanning when
// callers pass an arbitrary or stale Catalog value.
var (
	modelIDIndexMu sync.RWMutex
	modelIDIndex   map[string]string
)

// Catalog lookup policy (issue #132)
//
// Gateway provider IDs (azure-openai, azure-foundry, vertex-ai) differ from
// model-catalog key prefixes (azure_openai, azure_foundry, azure, vertex_ai).
//
// Two entry points:
//   - Get — metadata (/v1/models enrichment). Returns the first matching row
//     for the provider-specific prefix chain, including rows without input pricing.
//   - GetForPricing / Calculate — cost. Walks the same prefix chain but skips
//     unpriced chat rows when a later prefix may have rates; also falls back from
//     an unpriced exact catalog-native key (e.g. azure_foundry/phi-4 → azure/Phi-4).
//
// Tradeoffs:
//   - Dedicated catalog prefixes (azure_foundry/, azure_openai/) are preferred
//     over azure/ so capabilities and cache billing match that surface (Foundry
//     gpt-4o has no prompt-cache price; OpenAI gpt-4o-mini has distinct rates).
//   - azure/ fallback is used only when the preferred prefix has no priced chat
//     row (phi-4 casing/pricing lives under azure/Phi-4).
//   - Metadata and pricing can therefore differ for the same gateway request key;
//     that is intentional, not an oversight.
//
// catalogProviderAliases maps gateway provider IDs to catalog prefix chains.
var catalogProviderAliases = map[string][]string{
	"azure-openai":  {"azure_openai", "azure"},
	"azure-foundry": {"azure_foundry", "azure"},
	"vertex-ai":     {"vertex_ai"},
}

// BuildIndex constructs the reverse modelID → key index for a catalog that
// was not loaded through [Load] or [parse] (e.g. in tests). Calling this
// is unnecessary when the catalog comes from [Load].
func BuildIndex(c Catalog) {
	idx := make(map[string]string, len(c))
	for key, m := range c {
		if _, exists := idx[m.ModelID]; !exists {
			idx[m.ModelID] = key
		}
	}
	modelIDIndexMu.Lock()
	defer modelIDIndexMu.Unlock()
	modelIDIndex = idx
}

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
	result, err := LoadWithInfo()
	return result.Catalog, err
}

// LoadWithInfo fetches the model catalog and returns metadata about whether
// the remote source or embedded fallback was used.
func LoadWithInfo() (LoadResult, error) {
	catalogURL := os.Getenv(CatalogURLEnv)
	if catalogURL == "" {
		catalogURL = defaultCatalogURL
	}

	if data, err := fetchRemote(catalogURL); err == nil {
		c, parseErr := parse(data)
		if parseErr == nil {
			return LoadResult{Catalog: c, Source: LoadSourceRemote, URL: catalogURL}, nil
		}
		slog.Warn("model catalog remote response could not be parsed; using embedded fallback", "url", CatalogURLForLog(catalogURL), "error", catalogLoadErrorForLog(parseErr, catalogURL)) //nolint:gosec // values are CR/LF-sanitized before logging.
	} else {
		slog.Warn("model catalog remote fetch failed; using embedded fallback", "url", CatalogURLForLog(catalogURL), "error", catalogLoadErrorForLog(err, catalogURL)) //nolint:gosec // values are CR/LF-sanitized before logging.
	}

	c, err := parse(bundledCatalog)
	if err != nil {
		return LoadResult{Source: LoadSourceFallback, URL: catalogURL}, err
	}
	return LoadResult{Catalog: c, Source: LoadSourceFallback, URL: catalogURL}, nil
}

func safeLogValue(value string) string {
	return strings.NewReplacer("\r", "\\r", "\n", "\\n").Replace(value)
}

var httpURLInLogMessage = regexp.MustCompile(`https?://[^\s"']+`)

// CatalogURLForLog returns a catalog URL safe for structured logs: userinfo and
// query parameters are stripped so tokens in FERRO_MODEL_CATALOG_URL are not leaked.
func CatalogURLForLog(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "<catalog-url>"
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	out := u.Scheme + "://" + u.Host + u.EscapedPath()
	return safeLogValue(out)
}

// URLForLog returns the configured catalog URL in a log-safe form.
func (r LoadResult) URLForLog() string {
	return CatalogURLForLog(r.URL)
}

func catalogLoadErrorForLog(err error, catalogURL string) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if catalogURL != "" {
		msg = strings.ReplaceAll(msg, catalogURL, CatalogURLForLog(catalogURL))
	}
	return safeLogValue(httpURLInLogMessage.ReplaceAllStringFunc(msg, CatalogURLForLog))
}

func fetchRemote(rawURL string) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("invalid catalog URL %q: must be http or https with a host", rawURL)
	}
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Get(rawURL) //nolint:gosec // URL scheme and host validated above
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
	// Build the reverse modelID → key index so that Get() can resolve
	// bare model IDs without scanning all entries.
	BuildIndex(c)
	return c, nil
}

func (c Catalog) lookupUnderPrefix(prefix, modelID string) (Model, bool) {
	key := prefix + "/" + modelID
	if m, ok := c[key]; ok {
		return m, true
	}
	return c.getUnderPrefixCaseInsensitive(prefix, modelID)
}

func (c Catalog) getWithCatalogPrefixes(prefixes []string, modelID string, forPricing bool) (Model, bool) {
	for i, prefix := range prefixes {
		m, ok := c.lookupUnderPrefix(prefix, modelID)
		if !ok {
			continue
		}
		if !forPricing || catalogEntryUsableForPricing(m, i+1 < len(prefixes)) {
			return m, true
		}
	}
	return Model{}, false
}

func catalogEntryUsableForPricing(m Model, morePrefixes bool) bool {
	if !morePrefixes {
		return true
	}
	if m.Mode != ModeChat {
		return true
	}
	return m.Pricing.InputPerMTokens != nil
}

func pricingEntryHasInputRate(m Model) bool {
	if m.Mode != ModeChat {
		return true
	}
	return m.Pricing.InputPerMTokens != nil
}

// catalogPricingFallbackPrefixes returns later prefixes from any alias chain
// that starts with prefix (e.g. azure_foundry → azure).
func catalogPricingFallbackPrefixes(prefix string) []string {
	var out []string
	for _, chain := range catalogProviderAliases {
		for i, p := range chain {
			if p == prefix {
				out = append(out, chain[i+1:]...)
				break
			}
		}
	}
	return out
}

// getUnderPrefixCaseInsensitive finds a catalog entry whose key is
// prefix+"/"+modelID, comparing the model segment with strings.EqualFold.
// Used after an aliased exact-key miss (e.g. azure/phi-4 → azure/Phi-4).
func (c Catalog) getUnderPrefixCaseInsensitive(prefix, modelID string) (Model, bool) {
	prefixKey := prefix + "/"
	for k, m := range c {
		if !strings.HasPrefix(k, prefixKey) {
			continue
		}
		if strings.EqualFold(strings.TrimPrefix(k, prefixKey), modelID) {
			return m, true
		}
	}
	return Model{}, false
}

func (c Catalog) resolveAliased(key string, forPricing bool) (Model, bool) {
	provider, modelID, ok := strings.Cut(key, "/")
	if !ok || provider == "" || modelID == "" {
		return Model{}, false
	}
	prefixes, ok := catalogProviderAliases[provider]
	if !ok {
		return Model{}, false
	}
	return c.getWithCatalogPrefixes(prefixes, modelID, forPricing)
}

// Get looks up a model by "provider/model-id" for metadata enrichment.
func (c Catalog) Get(key string) (Model, bool) {
	return c.resolve(key, false)
}

// GetForPricing looks up a model for cost calculation.
func (c Catalog) GetForPricing(key string) (Model, bool) {
	return c.resolve(key, true)
}

func (c Catalog) resolve(key string, forPricing bool) (Model, bool) {
	provider, modelID, qualified := strings.Cut(key, "/")

	if m, ok := c[key]; ok {
		if !forPricing {
			return m, true
		}
		if pricingEntryHasInputRate(m) {
			return m, true
		}
		if qualified && provider != "" && modelID != "" {
			if fallbacks := catalogPricingFallbackPrefixes(provider); len(fallbacks) > 0 {
				if priced, ok := c.getWithCatalogPrefixes(fallbacks, modelID, true); ok {
					return priced, true
				}
			}
		}
		return m, true
	}

	if m, ok := c.resolveAliased(key, forPricing); ok {
		return m, true
	}
	// Bare model ID: use the reverse index for constant-time lookup.
	modelIDIndexMu.RLock()
	if idxKey, ok := modelIDIndex[key]; ok {
		modelIDIndexMu.RUnlock()
		if m, ok := c[idxKey]; ok && m.ModelID == key {
			return m, true
		}
	} else {
		modelIDIndexMu.RUnlock()
	}
	// Arbitrary Catalog values may not have an index, or another catalog load
	// may have replaced the package index. Preserve the pre-index behavior.
	for _, v := range c {
		if v.ModelID == key {
			return v, true
		}
	}
	return Model{}, false
}

// CatalogPrefixesFor returns the catalog key-prefix chain for a gateway provider
// ID. Gateway provider IDs (azure-openai, azure-foundry, vertex-ai) differ from
// model-catalog key prefixes (azure_openai, azure, …); aliased IDs return their
// full chain while every other ID maps to itself. The returned slice is always a
// fresh copy, so callers may mutate it without corrupting catalogProviderAliases.
func CatalogPrefixesFor(providerID string) []string {
	if chain, ok := catalogProviderAliases[providerID]; ok {
		out := make([]string, len(chain))
		copy(out, chain)
		return out
	}
	return []string{providerID}
}

// ModelsForProvider returns the sorted, de-duplicated bare model IDs of every
// catalog entry whose Provider matches any prefix in the gateway provider's
// catalog prefix chain (see [CatalogPrefixesFor]). All lifecycle states are
// included — deprecation is surfaced separately by the /v1/models response.
// An unknown provider with no entries yields an empty slice.
func (c Catalog) ModelsForProvider(providerID string) []string {
	prefixes := CatalogPrefixesFor(providerID)
	wanted := make(map[string]struct{}, len(prefixes))
	for _, p := range prefixes {
		wanted[p] = struct{}{}
	}

	seen := make(map[string]struct{})
	ids := make([]string, 0)
	for _, m := range c {
		if _, ok := wanted[m.Provider]; !ok {
			continue
		}
		if _, dup := seen[m.ModelID]; dup {
			continue
		}
		seen[m.ModelID] = struct{}{}
		ids = append(ids, m.ModelID)
	}
	sort.Strings(ids)
	return ids
}

// IsDeprecated returns true when the model's lifecycle status is deprecated or legacy.
func (m Model) IsDeprecated() bool {
	return m.Lifecycle.Status == "deprecated" || m.Lifecycle.Status == "legacy"
}
