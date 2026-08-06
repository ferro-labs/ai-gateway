package plugin

import (
	"fmt"
	"sync"
)

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

// RegisterFactory registers a plugin factory by name. Panics on an empty
// name, a nil factory, or duplicate registration; duplicates indicate a
// programming error (two plugins claim the same name).
//
// Plugins call this from their package init() function:
//
//	func init() {
//	    plugin.RegisterFactory("word-filter", New)
//	}
func RegisterFactory(name string, factory PluginFactory) {
	if name == "" || factory == nil {
		panic("plugin: RegisterFactory requires a non-empty name and non-nil factory")
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := pluginRegistry[name]; exists {
		panic(fmt.Sprintf("plugin: factory %q already registered", name))
	}
	pluginRegistry[name] = factory
}

// GetFactory returns a plugin factory by name.
func GetFactory(name string) (PluginFactory, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	f, ok := pluginRegistry[name]
	return f, ok
}

// ConfigValidator is implemented by a plugin whose config block has invariants
// that can be checked without a deployment. Implementing it moves those
// invariants from "the gateway fails to start" to "ferrogw validate says so",
// which is where an operator wants to meet them.
//
// The contract is deliberately narrow, because the caller is a pre-flight
// command that must produce the same answer on a build machine with no secrets
// as it does in production: ValidateConfig must not resolve ${VAR} references,
// must not perform I/O, and must not retain the config or mutate the receiver.
// It is called on a fresh instance from the registered factory, which is
// discarded without Init or Close. Anything the check cannot answer without the
// environment belongs in Init instead.
//
// ValidateConfig must accept every config Init accepts and reject every config
// Init rejects, so implementations share one parser between the two rather than
// restating the rules.
type ConfigValidator interface {
	ValidateConfig(config map[string]any) error
}

// ValidateConfigFor checks one plugin's config block against the plugin's own
// rules, when the registered plugin publishes any. Unknown plugin names and
// plugins that implement no validation are reported valid — this answers "is
// this block well-formed for that plugin", not "does that plugin exist", which
// the caller checks separately.
func ValidateConfigFor(name string, config map[string]any) error {
	factory, ok := GetFactory(name)
	if !ok {
		return nil
	}
	v, ok := factory().(ConfigValidator)
	if !ok {
		return nil
	}
	return v.ValidateConfig(config)
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
