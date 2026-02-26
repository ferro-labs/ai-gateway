package plugin

// PluginFactory creates a new instance of a plugin.
//nolint:revive // keep for backwards compatibility
type PluginFactory func() Plugin

// pluginRegistry is the global registry of plugin factories.
var pluginRegistry = map[string]PluginFactory{}

// RegisterFactory registers a plugin factory by name.
func RegisterFactory(name string, factory PluginFactory) {
	pluginRegistry[name] = factory
}

// GetFactory returns a plugin factory by name.
func GetFactory(name string) (PluginFactory, bool) {
	f, ok := pluginRegistry[name]
	return f, ok
}

// RegisteredPlugins returns the names of all registered plugin factories.
func RegisteredPlugins() []string {
	names := make([]string, 0, len(pluginRegistry))
	for name := range pluginRegistry {
		names = append(names, name)
	}
	return names
}
