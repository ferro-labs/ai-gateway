package models

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/pkg/logger"
)

// TestCatalogBackupParseable verifies the embedded catalog_backup.json is
// valid JSON that unmarshals into a non-empty Catalog. This is the gate
// checked before every release tag (see release checklist in the implementation
// guide).
func TestCatalogBackupParseable(t *testing.T) {
	c, err := parse(bundledCatalog)
	if err != nil {
		t.Fatalf("catalog_backup.json failed to parse: %v", err)
	}
	if len(c) == 0 {
		t.Fatal("catalog_backup.json parsed to an empty catalog")
	}
	t.Logf("catalog_backup.json OK — %d entries", len(c))
}

// TestCatalogRequiredFields checks that every entry in the backup has the
// mandatory fields filled in (provider, model_id, mode). The source field
// is present in most but not all entries and is logged as informational.
func TestCatalogRequiredFields(t *testing.T) {
	c, err := parse(bundledCatalog)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	noSource := 0
	for key, m := range c {
		if m.Provider == "" {
			t.Errorf("%s: missing provider", key)
		}
		if m.ModelID == "" {
			t.Errorf("%s: missing model_id", key)
		}
		if m.Mode == "" {
			t.Errorf("%s: missing mode", key)
		}
		if m.Source == "" {
			noSource++
		}
	}
	if noSource > 0 {
		t.Logf("INFO: %d/%d entries have no source URL — not a hard requirement", noSource, len(c))
	}
}

// TestCatalogNullVsZero logs entries from known paid providers that have a
// zero (not null) pricing field. Zero means "genuinely free"; null means
// "not applicable or unknown". This is informational — it helps maintainers
// spot LiteLLM data-quality issues without blocking CI.
func TestCatalogNullVsZero(t *testing.T) {
	c, err := parse(bundledCatalog)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// Only flag models from providers that charge real money.
	// Everyone else may legitimately bill $0.
	paidProviders := map[string]bool{
		"openai":    true,
		"anthropic": true,
		"groq":      true,
		"mistral":   true,
		"cohere":    true,
		"deepseek":  true,
		"replicate": true,
		"ai21":      true,
	}

	count := 0
	for key, m := range c {
		if !paidProviders[m.Provider] {
			continue
		}
		p := m.Pricing
		check := func(field string, v *float64) {
			if v != nil && *v == 0 {
				t.Logf("WARN %s: %s is 0.0 — should be null if not applicable or a real $0 price", key, field)
				count++
			}
		}
		check("input_per_m_tokens", p.InputPerMTokens)
		check("output_per_m_tokens", p.OutputPerMTokens)
		check("embedding_per_m_tokens", p.EmbeddingPerMTokens)
	}
	if count > 0 {
		t.Logf("Found %d pricing fields set to 0.0 in paid providers — review if intentional", count)
	}
}

func TestDefaultCatalogURLUsesModelCatalogReleaseAsset(t *testing.T) {
	if !strings.Contains(defaultCatalogURL, "github.com/ferro-labs/model-catalog/releases/latest/download/catalog.json") {
		t.Fatalf("defaultCatalogURL = %q, want model-catalog latest release asset", defaultCatalogURL)
	}
}

func TestCatalogURLForLog(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "strips userinfo and query",
			raw:  "https://user@catalog.example.com/v1/catalog.json?debug=true",
			want: "https://catalog.example.com/v1/catalog.json",
		},
		{
			name: "keeps public github asset path",
			raw:  defaultCatalogURL,
			want: "https://github.com/ferro-labs/model-catalog/releases/latest/download/catalog.json",
		},
		{name: "empty", raw: "", want: ""},
		{name: "invalid", raw: "not-a-url", want: "<catalog-url>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CatalogURLForLog(tt.raw); got != tt.want {
				t.Fatalf("CatalogURLForLog(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestLoadWithInfoUsesRemoteCatalog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"test/remote":{"provider":"test","model_id":"remote","mode":"chat"}}`))
	}))
	t.Cleanup(server.Close)
	t.Setenv(CatalogURLEnv, server.URL)

	result, err := LoadWithInfo()
	if err != nil {
		t.Fatalf("LoadWithInfo returned error: %v", err)
	}
	if result.Source != LoadSourceRemote {
		t.Fatalf("Source = %q, want %q", result.Source, LoadSourceRemote)
	}
	if result.URL != server.URL {
		t.Fatalf("URL = %q, want %q", result.URL, server.URL)
	}
	model, ok := result.Catalog.Get("test/remote")
	if !ok {
		t.Fatal("remote catalog model not found")
	}
	if model.ModelID != "remote" {
		t.Fatalf("ModelID = %q, want remote", model.ModelID)
	}
}

// TestLoadWithInfoContext_HonorsCanceledContext proves the ctx passed to
// LoadWithInfoContext is actually wired into the outbound HTTP request (not
// just accepted and ignored): with an already-canceled context, the request
// must never reach the server at all, and the result must fall back to the
// embedded catalog exactly like any other remote-fetch failure.
func TestLoadWithInfoContext_HonorsCanceledContext(t *testing.T) {
	var reached atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Store(true)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"test/remote":{"provider":"test","model_id":"remote","mode":"chat"}}`))
	}))
	t.Cleanup(server.Close)
	t.Setenv(CatalogURLEnv, server.URL)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	result, err := LoadWithInfoContext(ctx)
	if err != nil {
		t.Fatalf("LoadWithInfoContext returned error: %v", err)
	}
	if result.Source != LoadSourceFallback {
		t.Fatalf("Source = %q, want %q (canceled context must not reach the server)", result.Source, LoadSourceFallback)
	}
	if reached.Load() {
		t.Fatal("server was reached despite an already-canceled context")
	}
}

func TestLoadWithInfoFallsBackWhenRemoteFetchFails(t *testing.T) {
	var buf bytes.Buffer
	previous := logger.Default()
	logger.SetDefault(logger.New(logger.Options{Output: &buf}))
	t.Cleanup(func() { logger.SetDefault(previous) })
	const secret = "super-secret-token"
	t.Setenv(CatalogURLEnv, "http://user:"+secret+"@127.0.0.1:1/catalog.json?api_key="+secret)

	result, err := LoadWithInfo()
	if err != nil {
		t.Fatalf("LoadWithInfo returned error: %v", err)
	}
	if result.Source != LoadSourceFallback {
		t.Fatalf("Source = %q, want %q", result.Source, LoadSourceFallback)
	}
	if len(result.Catalog) == 0 {
		t.Fatal("fallback catalog is empty")
	}
	logged := buf.String()
	if !strings.Contains(logged, "using embedded fallback") {
		t.Fatalf("fallback warning was not logged: %s", logged)
	}
	if strings.Contains(logged, secret) {
		t.Fatalf("catalog URL secret leaked into logs: %s", logged)
	}
	if !strings.Contains(logged, "http://127.0.0.1:1/catalog.json") {
		t.Fatalf("expected redacted host/path in logs, got: %s", logged)
	}
}

func TestLoadWithInfoFallsBackWhenRemoteParseFails(t *testing.T) {
	var buf bytes.Buffer
	previous := logger.Default()
	logger.SetDefault(logger.New(logger.Options{Output: &buf}))
	t.Cleanup(func() { logger.SetDefault(previous) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`not json`))
	}))
	t.Cleanup(server.Close)
	t.Setenv(CatalogURLEnv, server.URL)

	result, err := LoadWithInfo()
	if err != nil {
		t.Fatalf("LoadWithInfo returned error: %v", err)
	}
	if result.Source != LoadSourceFallback {
		t.Fatalf("Source = %q, want %q", result.Source, LoadSourceFallback)
	}
	if len(result.Catalog) == 0 {
		t.Fatal("fallback catalog is empty")
	}
	if !strings.Contains(buf.String(), "could not be parsed") {
		t.Fatalf("parse fallback warning was not logged: %s", buf.String())
	}
}

func TestLoadWithInfoFallsBackForInvalidOverrideURL(t *testing.T) {
	t.Setenv(CatalogURLEnv, "file:///tmp/catalog.json")

	result, err := LoadWithInfo()
	if err != nil {
		t.Fatalf("LoadWithInfo returned error: %v", err)
	}
	if result.Source != LoadSourceFallback {
		t.Fatalf("Source = %q, want %q", result.Source, LoadSourceFallback)
	}
}

// TestCatalogGet verifies the Get() helper finds keys both with and without
// the provider prefix.
func TestCatalogGet(t *testing.T) {
	c := Catalog{
		"openai/gpt-4o": {
			Provider: "openai",
			ModelID:  "gpt-4o",
			Mode:     ModeChat,
		},
	}
	BuildIndex(c)

	if _, ok := c.Get("openai/gpt-4o"); !ok {
		t.Error("Get with provider prefix should succeed")
	}
	if _, ok := c.Get("gpt-4o"); !ok {
		t.Error("Get with bare model ID should succeed via reverse index")
	}
	if _, ok := c.Get("nonexistent-model"); ok {
		t.Error("Get with unknown model should return false")
	}
}

func TestCatalogGetProviderAlias(t *testing.T) {
	cacheRead := 1.25
	c := Catalog{
		"azure/gpt-4o": {
			Provider: "azure",
			ModelID:  "gpt-4o",
			Mode:     ModeChat,
			Pricing: Pricing{
				InputPerMTokens:     ptrF(2.5),
				CacheReadPerMTokens: &cacheRead,
			},
			Capabilities: Capabilities{PromptCaching: true},
		},
		"azure_foundry/gpt-4o": {
			Provider: "azure_foundry",
			ModelID:  "gpt-4o",
			Mode:     ModeChat,
			Pricing: Pricing{
				InputPerMTokens: ptrF(2.5),
			},
			Capabilities: Capabilities{PromptCaching: false},
		},
		"vertex_ai/gemini-2.5-pro": {
			Provider: "vertex_ai",
			ModelID:  "gemini-2.5-pro",
			Mode:     ModeChat,
		},
		"azure_openai/gpt-4o-mini": {
			Provider: "azure_openai",
			ModelID:  "gpt-4o-mini",
			Mode:     ModeChat,
			Pricing: Pricing{
				InputPerMTokens: ptrF(0.15),
			},
		},
		"azure/gpt-4o-mini": {
			Provider: "azure",
			ModelID:  "gpt-4o-mini",
			Mode:     ModeChat,
			Pricing: Pricing{
				InputPerMTokens: ptrF(0.165),
			},
		},
		"hugging_face/meta-llama/Meta-Llama-3.1-8B-Instruct": {
			Provider: "hugging_face",
			ModelID:  "meta-llama/Meta-Llama-3.1-8B-Instruct",
			Mode:     ModeChat,
		},
		"nvidia_nim/meta/llama-3.1-8b-instruct": {
			Provider: "nvidia_nim",
			ModelID:  "meta/llama-3.1-8b-instruct",
			Mode:     ModeChat,
		},
		"ollama_cloud/gpt-oss:120b": {
			Provider: "ollama_cloud",
			ModelID:  "gpt-oss:120b",
			Mode:     ModeChat,
		},
		"dashscope/qwen3-max": {
			Provider: "dashscope",
			ModelID:  "qwen3-max",
			Mode:     ModeChat,
		},
	}

	cases := []struct {
		key      string
		provider string
		modelID  string
	}{
		{"azure-openai/gpt-4o-mini", "azure_openai", "gpt-4o-mini"},
		{"azure-foundry/gpt-4o", "azure_foundry", "gpt-4o"},
		{"vertex-ai/gemini-2.5-pro", "vertex_ai", "gemini-2.5-pro"},
		{"hugging-face/meta-llama/Meta-Llama-3.1-8B-Instruct", "hugging_face", "meta-llama/Meta-Llama-3.1-8B-Instruct"},
		{"nvidia-nim/meta/llama-3.1-8b-instruct", "nvidia_nim", "meta/llama-3.1-8b-instruct"},
		{"ollama-cloud/gpt-oss:120b", "ollama_cloud", "gpt-oss:120b"},
		{"qwen/qwen3-max", "dashscope", "qwen3-max"},
	}
	if len(cases) != len(catalogProviderAliases) {
		t.Fatalf("test cases = %d, catalogProviderAliases = %d — add a case per alias",
			len(cases), len(catalogProviderAliases))
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			got, ok := c.Get(tc.key)
			if !ok {
				t.Fatalf("Get(%q) should succeed", tc.key)
			}
			if got.Provider != tc.provider || got.ModelID != tc.modelID {
				t.Fatalf("Get(%q) = (%q,%q), want (%q,%q)",
					tc.key, got.Provider, got.ModelID, tc.provider, tc.modelID)
			}
		})
	}

	for gatewayID := range catalogProviderAliases {
		t.Run(gatewayID+"/unknown", func(t *testing.T) {
			if _, ok := c.Get(gatewayID + "/unknown-model"); ok {
				t.Fatalf("Get(%q/unknown-model) should not succeed", gatewayID)
			}
		})
	}
}

func TestCatalogGetProviderAliasCaseInsensitive(t *testing.T) {
	c := Catalog{
		"azure/Phi-4": {
			Provider: "azure",
			ModelID:  "Phi-4",
			Mode:     ModeChat,
			Pricing: Pricing{
				InputPerMTokens:  ptrF(0.125),
				OutputPerMTokens: ptrF(0.5),
			},
		},
	}

	got, ok := c.Get("azure-foundry/phi-4")
	if !ok {
		t.Fatal("Get(azure-foundry/phi-4) should succeed via case-insensitive alias")
	}
	if got.ModelID != "Phi-4" {
		t.Fatalf("ModelID = %q, want Phi-4", got.ModelID)
	}
	if got.Pricing.InputPerMTokens == nil || *got.Pricing.InputPerMTokens != 0.125 {
		t.Fatalf("expected priced azure/Phi-4 entry, got %+v", got.Pricing)
	}
}

func TestCatalogGetAzureFoundryPrefersFoundryCatalog(t *testing.T) {
	c, err := parse(bundledCatalog)
	if err != nil {
		t.Fatalf("parse bundled catalog: %v", err)
	}

	got, ok := c.Get("azure-foundry/gpt-4o")
	if !ok {
		t.Fatal("embedded catalog should resolve azure-foundry/gpt-4o")
	}
	if got.Provider != "azure_foundry" {
		t.Fatalf("Provider = %q, want azure_foundry", got.Provider)
	}
	if got.Capabilities.PromptCaching {
		t.Fatal("expected azure_foundry/gpt-4o entry without prompt caching")
	}
	if got.Pricing.CacheReadPerMTokens != nil {
		t.Fatal("expected azure_foundry entry without cache-read pricing")
	}
}

func TestCatalogGetAzureFoundryPhi4MetadataEmbeddedCatalog(t *testing.T) {
	c, err := parse(bundledCatalog)
	if err != nil {
		t.Fatalf("parse bundled catalog: %v", err)
	}

	got, ok := c.Get("azure-foundry/phi-4")
	if !ok {
		t.Fatal("embedded catalog should resolve azure-foundry/phi-4 for metadata")
	}
	if got.Provider != "azure_foundry" {
		t.Fatalf("Provider = %q, want azure_foundry", got.Provider)
	}
	if got.MaxOutputTokens != 4096 {
		t.Fatalf("MaxOutputTokens = %d, want Foundry metadata (4096)", got.MaxOutputTokens)
	}
}

func TestCatalogGetForPricingAzureFoundryPhi4EmbeddedCatalog(t *testing.T) {
	c, err := parse(bundledCatalog)
	if err != nil {
		t.Fatalf("parse bundled catalog: %v", err)
	}

	got, ok := c.GetForPricing("azure-foundry/phi-4")
	if !ok {
		t.Fatal("embedded catalog should resolve azure-foundry/phi-4 for pricing")
	}
	if got.ModelID != "Phi-4" {
		t.Fatalf("ModelID = %q, want Phi-4", got.ModelID)
	}
	if got.Pricing.InputPerMTokens == nil {
		t.Fatal("expected priced azure/Phi-4 entry, not azure_foundry/phi-4 null pricing")
	}
}

func TestCatalogGetAzureOpenAIPrefersOpenAICatalog(t *testing.T) {
	c, err := parse(bundledCatalog)
	if err != nil {
		t.Fatalf("parse bundled catalog: %v", err)
	}

	got, ok := c.Get("azure-openai/gpt-4o-mini")
	if !ok {
		t.Fatal("embedded catalog should resolve azure-openai/gpt-4o-mini")
	}
	if got.Provider != "azure_openai" {
		t.Fatalf("Provider = %q, want azure_openai", got.Provider)
	}
	if got.Pricing.InputPerMTokens == nil || *got.Pricing.InputPerMTokens != 0.15 {
		t.Fatalf("input price = %v, want 0.15 from azure_openai entry", got.Pricing.InputPerMTokens)
	}
}

func ptrF(v float64) *float64 { return &v }

func TestCatalogGetDirectCatalogFallsBackToScan(t *testing.T) {
	BuildIndex(Catalog{})
	c := Catalog{
		"custom/custom-model": {
			Provider: "custom",
			ModelID:  "custom-model",
			Mode:     ModeChat,
		},
	}

	if _, ok := c.Get("custom-model"); !ok {
		t.Fatal("Get with bare model ID should succeed without BuildIndex")
	}
}

func TestCatalogGetStaleIndexFallsBackToScan(t *testing.T) {
	BuildIndex(Catalog{
		"old/provider-model": {
			Provider: "old",
			ModelID:  "same-model",
			Mode:     ModeChat,
		},
	})
	c := Catalog{
		"new/provider-model": {
			Provider: "new",
			ModelID:  "same-model",
			Mode:     ModeChat,
		},
	}

	got, ok := c.Get("same-model")
	if !ok {
		t.Fatal("Get with bare model ID should succeed with stale index")
	}
	if got.Provider != "new" {
		t.Fatalf("Get returned provider %q, want new", got.Provider)
	}
}

func TestCatalogGetStaleIndexValidatesModelID(t *testing.T) {
	BuildIndex(Catalog{
		"shared/key": {
			Provider: "old",
			ModelID:  "old-model",
			Mode:     ModeChat,
		},
	})
	c := Catalog{
		"shared/key": {
			Provider: "new",
			ModelID:  "new-model",
			Mode:     ModeChat,
		},
	}

	if _, ok := c.Get("old-model"); ok {
		t.Fatal("Get should not return stale indexed key with a different model ID")
	}
}

func TestCatalogGetConcurrentBuildIndex(t *testing.T) {
	c := Catalog{
		"new/provider-model": {
			Provider: "new",
			ModelID:  "same-model",
			Mode:     ModeChat,
		},
	}
	other := Catalog{
		"old/provider-model": {
			Provider: "old",
			ModelID:  "same-model",
			Mode:     ModeChat,
		},
	}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			BuildIndex(other)
		}()
		go func() {
			defer wg.Done()
			if _, ok := c.Get("same-model"); !ok {
				t.Error("Get with bare model ID should succeed during index rebuild")
			}
		}()
	}
	wg.Wait()
}

// TestIsDeprecated checks that both "deprecated" and "legacy" statuses are
// treated as deprecated, while "ga" and "preview" are not.
func TestIsDeprecated(t *testing.T) {
	cases := []struct {
		status string
		want   bool
	}{
		{"deprecated", true},
		{"legacy", true},
		{"ga", false},
		{"preview", false},
		{"", false},
	}
	for _, tc := range cases {
		m := Model{Lifecycle: Lifecycle{Status: tc.status}}
		if got := m.IsDeprecated(); got != tc.want {
			t.Errorf("IsDeprecated(%q) = %v, want %v", tc.status, got, tc.want)
		}
	}
}

// assertSortedDeduped fails the test if s is not sorted ascending or contains
// adjacent duplicates.
func assertSortedDeduped(t *testing.T, s []string) {
	t.Helper()
	if !sort.StringsAreSorted(s) {
		t.Fatalf("result is not sorted ascending: %v", s)
	}
	for i := 1; i < len(s); i++ {
		if s[i] == s[i-1] {
			t.Fatalf("result contains adjacent duplicate %q: %v", s[i], s)
		}
	}
}

// TestCatalogPrefixesFor verifies the gateway-provider-ID → catalog-prefix-chain
// mapping, including aliased and identity providers.
func TestCatalogPrefixesFor(t *testing.T) {
	cases := []struct {
		providerID string
		want       []string
	}{
		{"azure-openai", []string{"azure_openai", "azure"}},
		{"azure-foundry", []string{"azure_foundry", "azure"}},
		{"vertex-ai", []string{"vertex_ai"}},
		{"anthropic", []string{"anthropic"}},
		{"does-not-exist", []string{"does-not-exist"}},
	}
	for _, tc := range cases {
		t.Run(tc.providerID, func(t *testing.T) {
			got := CatalogPrefixesFor(tc.providerID)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("CatalogPrefixesFor(%q) = %v, want %v", tc.providerID, got, tc.want)
			}
		})
	}
}

// TestCatalogPrefixesForDoesNotExposeInternalSlice verifies the returned slice
// is a copy — mutating it must not corrupt the package-level alias map.
func TestCatalogPrefixesForDoesNotExposeInternalSlice(t *testing.T) {
	got := CatalogPrefixesFor("azure-openai")
	if len(got) == 0 {
		t.Fatal("expected non-empty prefix chain")
	}
	got[0] = "MUTATED"

	again := CatalogPrefixesFor("azure-openai")
	if !reflect.DeepEqual(again, []string{"azure_openai", "azure"}) {
		t.Fatalf("internal alias slice was mutated: got %v", again)
	}
}

// TestModelsForProvider verifies the catalog→provider listing helper against the
// bundled catalog: correct counts, sorted/deduped output, and identity vs alias
// resolution.
func TestModelsForProvider(t *testing.T) {
	c, err := parse(bundledCatalog)
	if err != nil {
		t.Fatalf("parse bundled catalog: %v", err)
	}

	// Counts are derived from the parsed catalog, not hardcoded: these are
	// identity providers, so the answer is by definition every row whose
	// Provider is the gateway ID. Literals here would fail on every upstream
	// catalog refresh without a gateway defect behind it.
	for _, providerID := range []string{"anthropic", "xai", "does-not-exist"} {
		t.Run(providerID, func(t *testing.T) {
			want := make(map[string]struct{})
			for _, m := range c {
				if m.Provider == providerID {
					want[m.ModelID] = struct{}{}
				}
			}
			got := c.ModelsForProvider(providerID)
			if len(got) != len(want) {
				t.Fatalf("ModelsForProvider(%q) returned %d models, want %d",
					providerID, len(got), len(want))
			}
			for _, id := range got {
				if _, ok := want[id]; !ok {
					t.Fatalf("ModelsForProvider(%q) returned %q, which no catalog row carries", providerID, id)
				}
			}
			assertSortedDeduped(t, got)
		})
	}
}

// TestActiveModelCountForProvider verifies the non-deprecated per-provider count:
// deprecated and legacy rows are excluded, bare model IDs are de-duplicated
// across the alias chain, and an unknown provider yields 0.
func TestActiveModelCountForProvider(t *testing.T) {
	c := Catalog{
		"groq/llama-3.3-70b":  {Provider: "groq", ModelID: "llama-3.3-70b"},
		"groq/llama-3.1-8b":   {Provider: "groq", ModelID: "llama-3.1-8b"},
		"groq/old-model":      {Provider: "groq", ModelID: "old-model", Lifecycle: Lifecycle{Status: "deprecated"}},
		"groq/legacy-model":   {Provider: "groq", ModelID: "legacy-model", Lifecycle: Lifecycle{Status: "legacy"}},
		"azure_openai/gpt-4o": {Provider: "azure_openai", ModelID: "gpt-4o"},
		"azure/gpt-4o":        {Provider: "azure", ModelID: "gpt-4o"}, // same bare ID via the alias chain
		"azure/gpt-4o-mini":   {Provider: "azure", ModelID: "gpt-4o-mini"},
	}

	if got := c.ActiveModelCountForProvider("groq"); got != 2 {
		t.Errorf("groq: got %d, want 2 (deprecated + legacy excluded)", got)
	}
	// azure-openai's chain is azure_openai + azure; gpt-4o is one deduped id.
	if got := c.ActiveModelCountForProvider("azure-openai"); got != 2 {
		t.Errorf("azure-openai: got %d, want 2 (gpt-4o deduped across the alias chain)", got)
	}
	if got := c.ActiveModelCountForProvider("does-not-exist"); got != 0 {
		t.Errorf("unknown provider: got %d, want 0", got)
	}
}

// TestModelsForProviderAliasUnion verifies that an aliased gateway provider ID
// returns the deduped union of model IDs across every catalog prefix in its
// alias chain (azure-openai → azure_openai + azure).
func TestModelsForProviderAliasUnion(t *testing.T) {
	c, err := parse(bundledCatalog)
	if err != nil {
		t.Fatalf("parse bundled catalog: %v", err)
	}

	// Compute the expected union directly from the parsed catalog.
	wantSet := make(map[string]struct{})
	for _, m := range c {
		if m.Provider == "azure_openai" || m.Provider == "azure" {
			wantSet[m.ModelID] = struct{}{}
		}
	}

	got := c.ModelsForProvider("azure-openai")
	if len(got) != len(wantSet) {
		t.Fatalf("ModelsForProvider(azure-openai) returned %d models, want %d (union of azure_openai + azure)",
			len(got), len(wantSet))
	}
	assertSortedDeduped(t, got)
	for _, id := range got {
		if _, ok := wantSet[id]; !ok {
			t.Fatalf("unexpected model %q not in azure_openai/azure union", id)
		}
	}
}

// TestCatalogReachesUnderscoreAndVendorBrandPrefixes covers the providers whose
// catalog prefix is not their gateway ID. Before these alias entries existed the
// rows below were unreachable from every entry point at once: ModelsForProvider
// returned nothing (so /v1/models listed the provider empty) and GetForPricing
// missed (so cost was recorded as zero). Deleting an entry from
// catalogProviderAliases fails this test.
func TestCatalogReachesUnderscoreAndVendorBrandPrefixes(t *testing.T) {
	c, err := parse(bundledCatalog)
	if err != nil {
		t.Fatalf("parse bundled catalog: %v", err)
	}

	cases := []struct {
		providerID  string
		sampleModel string
	}{
		{"hugging-face", "Qwen/Qwen2.5-72B-Instruct"},
		{"nvidia-nim", "meta/llama-3.1-8b-instruct"},
		{"ollama-cloud", "gpt-oss:120b"},
		// dashscope is Alibaba's own API — the one providers/qwen talks to —
		// so its rows must answer for the gateway's qwen provider.
		{"qwen", "qwen3-max"},
	}
	for _, tc := range cases {
		t.Run(tc.providerID, func(t *testing.T) {
			ids := c.ModelsForProvider(tc.providerID)
			if !slices.Contains(ids, tc.sampleModel) {
				t.Fatalf("ModelsForProvider(%q) does not list %q (got %d ids)",
					tc.providerID, tc.sampleModel, len(ids))
			}
			if _, ok := c.Get(tc.providerID + "/" + tc.sampleModel); !ok {
				t.Fatalf("Get(%q) missed", tc.providerID+"/"+tc.sampleModel)
			}
		})
	}

	// The pricing half of the same fix: qwq-plus is priced only under
	// dashscope/, so before the alias every request for it billed zero.
	m, ok := c.GetForPricing("qwen/qwq-plus")
	if !ok {
		t.Fatal("GetForPricing(qwen/qwq-plus) missed")
	}
	if m.Pricing.InputPerMTokens == nil || *m.Pricing.InputPerMTokens <= 0 {
		t.Fatalf("qwen/qwq-plus resolved with no input rate: %+v", m.Pricing)
	}

	// The qwen/ rows the catalog already carried must survive the chain gaining
	// dashscope — an alias that replaced them would unlist five models.
	if _, ok := c.Get("qwen/qwen2.5-72b-instruct"); !ok {
		t.Fatal("Get(qwen/qwen2.5-72b-instruct) missed: native qwen/ rows were dropped")
	}
}

// TestModelsForProviderIncludesDeprecated verifies deprecated entries are NOT
// filtered out (the /v1/models response flags deprecation separately).
func TestModelsForProviderIncludesDeprecated(t *testing.T) {
	deprecated := Catalog{
		"toy/active-model": {
			Provider:  "toy",
			ModelID:   "active-model",
			Mode:      ModeChat,
			Lifecycle: Lifecycle{Status: "ga"},
		},
		"toy/old-model": {
			Provider:  "toy",
			ModelID:   "old-model",
			Mode:      ModeChat,
			Lifecycle: Lifecycle{Status: "deprecated"},
		},
	}

	got := deprecated.ModelsForProvider("toy")
	want := []string{"active-model", "old-model"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ModelsForProvider(toy) = %v, want %v (deprecated must be included)", got, want)
	}
}

// TestCatalogFetchTimeoutEnv_DisablesRemoteFetch covers the escape hatch for
// deployments whose egress is blocked. Without it, every start would wait the
// full default budget for a fetch that cannot succeed — and it waits before the
// gateway binds its listener, so nothing answers a readiness probe meanwhile.
func TestCatalogFetchTimeoutEnv_DisablesRemoteFetch(t *testing.T) {
	var reached atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Store(true)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"test/remote":{"provider":"test","model_id":"remote","mode":"chat"}}`))
	}))
	t.Cleanup(server.Close)
	t.Setenv(CatalogURLEnv, server.URL)
	t.Setenv(CatalogFetchTimeoutEnv, "0")

	result, err := LoadWithInfo()
	if err != nil {
		t.Fatalf("LoadWithInfo returned error: %v", err)
	}
	if reached.Load() {
		t.Fatal("remote catalog was fetched despite the fetch being disabled")
	}
	if result.Source != LoadSourceFallback {
		t.Fatalf("Source = %q, want %q", result.Source, LoadSourceFallback)
	}
	if len(result.Catalog) == 0 {
		t.Fatal("expected the embedded catalog to be served when the fetch is disabled")
	}
}

// TestResolveCatalogFetchTimeout covers the parsing of the override. An
// unparseable value keeps the default rather than failing the load: the catalog
// is best-effort, and a typo in an optional knob must not silently change how
// long startup blocks.
func TestResolveCatalogFetchTimeout(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want time.Duration
	}{
		{"unset keeps the default", "", catalogFetchTimeout},
		{"explicit shorter budget", "250ms", 250 * time.Millisecond},
		{"zero disables the fetch", "0", 0},
		{"negative disables the fetch", "-1s", 0},
		{"unparseable keeps the default", "soon", catalogFetchTimeout},
		{"surrounding whitespace tolerated", "  2s  ", 2 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(CatalogFetchTimeoutEnv, tt.env)
			if got := resolveCatalogFetchTimeout(); got != tt.want {
				t.Fatalf("resolveCatalogFetchTimeout() = %v, want %v", got, tt.want)
			}
		})
	}
}
