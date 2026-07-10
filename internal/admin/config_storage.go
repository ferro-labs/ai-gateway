package admin

import (
	"errors"
	"fmt"
	"maps"

	aigateway "github.com/ferro-labs/ai-gateway"
)

// storageOptionKeys are the plugin options that name where the gateway reads
// and writes: a filesystem path or a database connection string.
var storageOptionKeys = []string{"dsn", "backend"}

// ErrStorageOptionChanged is returned when a config submitted over the admin API
// tries to change a plugin's storage location.
var ErrStorageOptionChanged = errors.New("plugin storage options cannot be changed over the admin API")

// resolveStorageOptions returns next with every plugin's storage options taken
// from the running config, and reports an error if the submitted config tried
// to change one.
//
// Where the gateway stores data is process-level configuration, set from the
// config file or the environment by whoever runs the process. It is not
// something an authenticated request may redirect: a request-supplied `dsn`
// reaches requestlog.NewSQLiteWriter, which creates a database file — and
// restricts its permissions — at whatever path it is given.
//
// The values are copied from the running config rather than merely compared
// against it, so no path from a request body can reach a plugin's Init even if
// a later caller is added that forgets to check the error.
func resolveStorageOptions(current, next aigateway.Config) (aigateway.Config, error) {
	running := make(map[string]map[string]any, len(current.Plugins))
	for _, p := range current.Plugins {
		running[p.Name] = p.Config
	}

	plugins := make([]aigateway.PluginConfig, len(next.Plugins))
	for i, submitted := range next.Plugins {
		resolved := make(map[string]any, len(submitted.Config))
		maps.Copy(resolved, submitted.Config)

		for _, key := range storageOptionKeys {
			active := stringOption(running[submitted.Name], key)
			if stringOption(submitted.Config, key) != active {
				return aigateway.Config{}, fmt.Errorf("%w: plugin %q, option %q", ErrStorageOptionChanged, submitted.Name, key)
			}
			if active == "" {
				delete(resolved, key)
				continue
			}
			resolved[key] = active
		}

		plugin := submitted
		plugin.Config = resolved
		plugins[i] = plugin
	}

	out := next
	out.Plugins = plugins
	return out, nil
}

// stringOption reads a string option, treating a missing key and a non-string
// value alike: neither names a storage location.
func stringOption(config map[string]any, key string) string {
	if config == nil {
		return ""
	}
	value, _ := config[key].(string)
	return value
}
