package models

import "testing"

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
