package observability

import (
	"fmt"
	"sync"
)

// ExporterFactory constructs a new Exporter instance. Plugins register
// a factory via RegisterExporter in their package init().
type ExporterFactory func() Exporter

var (
	registryMu sync.RWMutex
	registry   = map[string]ExporterFactory{}
)

// RegisterExporter registers a factory under name. Panics on duplicate
// registration; duplicates indicate a programming error (two plugins
// claim the same name).
//
// Plugins call this from their package init() function:
//
//	func init() {
//	    observability.RegisterExporter("langsmith", New)
//	}
func RegisterExporter(name string, factory ExporterFactory) {
	if name == "" || factory == nil {
		panic("observability: RegisterExporter requires a non-empty name and non-nil factory")
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := registry[name]; exists {
		panic(fmt.Sprintf("observability: exporter %q already registered", name))
	}
	registry[name] = factory
}

// LookupExporter returns the factory for name, or (nil, false) if not
// registered.
func LookupExporter(name string) (ExporterFactory, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	f, ok := registry[name]
	return f, ok
}

// RegisteredExporters returns the names of all registered exporters in
// unspecified order.
func RegisteredExporters() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	return names
}

// resetRegistryForTest clears the registry; used by in-package tests only.
func resetRegistryForTest() {
	registryMu.Lock()
	registry = map[string]ExporterFactory{}
	registryMu.Unlock()
}
