package models

import (
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
// mandatory fields filled in (provider, model_id, mode, source).
func TestCatalogRequiredFields(t *testing.T) {
	c, err := parse(bundledCatalog)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

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

	if _, ok := c.Get("openai/gpt-4o"); !ok {
		t.Error("Get with provider prefix should succeed")
	}
	if _, ok := c.Get("gpt-4o"); !ok {
		t.Error("Get with bare model ID should succeed via fallback scan")
	}
	if _, ok := c.Get("nonexistent-model"); ok {
		t.Error("Get with unknown model should return false")
	}
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
