package models

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
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

func TestLoadWithInfoFallsBackWhenRemoteFetchFails(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
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
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
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
	}

	cases := []struct {
		key      string
		provider string
		modelID  string
	}{
		{"azure-openai/gpt-4o-mini", "azure_openai", "gpt-4o-mini"},
		{"azure-foundry/gpt-4o", "azure_foundry", "gpt-4o"},
		{"vertex-ai/gemini-2.5-pro", "vertex_ai", "gemini-2.5-pro"},
	}
	if len(cases) != len(catalogProviderAliases) {
		t.Fatalf("test cases = %d, catalogProviderAliases = %d — add a case per alias",
			len(cases), len(catalogProviderAliases))
	}
	for _, tc := range cases {
		tc := tc
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
		gatewayID := gatewayID
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

// BenchmarkGetBareModelID benchmarks the bare-model-ID lookup path, which
// now uses the reverse index instead of a linear scan.
func BenchmarkGetBareModelID(b *testing.B) {
	c, err := parse(bundledCatalog)
	if err != nil {
		b.Fatalf("parse: %v", err)
	}
	// Pick a model ID that exists in the catalog.
	m, ok := c.Get("openai/gpt-4o")
	if !ok {
		b.Fatal("gpt-4o not found in catalog")
	}
	bareID := m.ModelID

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Get(bareID)
	}
}
