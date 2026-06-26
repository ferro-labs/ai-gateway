package plugin

import "sync"

// PluginFactory creates a new instance of a plugin.
//
//nolint:revive // keep for backwards compatibility
type PluginFactory func() Plugin

// pluginRegistry is the global registry of plugin factories. It is guarded by
// registryMu because RegisterFactory (typically called from plugin init()
// functions, which may run on different goroutines) writes to the map while
// GetFactory/RegisteredPlugins read from it. Without the lock, concurrent
// access is a data race and Go panics on concurrent map read/write. This
// mirrors the guarded pattern in observability/registry.go.
var (
	registryMu     sync.RWMutex
	pluginRegistry = map[string]PluginFactory{}
)

// RegisterFactory registers a plugin factory by name.
func RegisterFactory(name string, factory PluginFactory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	pluginRegistry[name] = factory
}

// GetFactory returns a plugin factory by name.
func GetFactory(name string) (PluginFactory, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	f, ok := pluginRegistry[name]
	return f, ok
}

// RegisteredPlugins returns the names of all registered plugin factories.
func RegisteredPlugins() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	names := make([]string, 0, len(pluginRegistry))
	for name := range pluginRegistry {
		names = append(names, name)
	}
	return names
}
