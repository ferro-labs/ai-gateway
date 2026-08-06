package plugin_test

import (
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/plugin"

	// Registered for their side effects, exactly as cmd/ferrogw imports them.
	// A seventh plugin is added to this list and is then held to every
	// assertion below.
	_ "github.com/ferro-labs/ai-gateway/plugin/budget"
	_ "github.com/ferro-labs/ai-gateway/plugin/cache"
	_ "github.com/ferro-labs/ai-gateway/plugin/logger"
	_ "github.com/ferro-labs/ai-gateway/plugin/maxtoken"
	_ "github.com/ferro-labs/ai-gateway/plugin/ratelimit"
	_ "github.com/ferro-labs/ai-gateway/plugin/wordfilter"
)

// configKeyPattern matches a plugin reading a key out of the config map it is
// handed. Every built-in reads its configuration in exactly this form, which is
// what makes the source itself usable as the authority on which keys exist.
var configKeyPattern = regexp.MustCompile(`config\["([a-z0-9_]+)"\]`)

// registerPattern matches a plugin claiming its registered name, which is how a
// name is mapped back to the package that implements it without the catalog
// having to carry a directory.
var registerPattern = regexp.MustCompile(`RegisterFactory\("([a-z0-9-]+)"`)

// undocumentedKeys are keys a plugin reads but deliberately does not advertise:
// both are obsolete request-logger options, accepted only so an operator running
// an old configuration gets a warning naming the environment variable that
// replaced them. Listing them in the catalog would invite their use.
var undocumentedKeys = map[string][]string{
	"request-logger": {"backend", "dsn"},
}

// TestCatalogCoversEveryBuiltinPlugin fails when a package under plugin/
// registers a name the catalog does not describe, or an entry survives the
// plugin it described.
//
// Measured against the packages on disk rather than against the registry,
// because the registry only holds what something imported: a seventh plugin
// whose blank import this file did not gain would register nothing here and
// pass a registry comparison while the catalog said nothing about it. The
// directory cannot be forgotten.
func TestCatalogCoversEveryBuiltinPlugin(t *testing.T) {
	described := make([]string, 0, len(plugin.Builtins()))
	for _, entry := range plugin.Builtins() {
		described = append(described, entry.Name)
	}
	onDisk := slices.Sorted(maps.Keys(pluginSources(t)))

	slices.Sort(described)

	if !slices.Equal(described, onDisk) {
		t.Errorf("catalog does not match the plugins under plugin/\ncatalog: %v\n on disk: %v\n"+
			"add or remove an entry in plugin/catalog.go", described, onDisk)
	}
}

// TestCatalogTypeMatchesReportedType fails when an entry's Type disagrees with
// what the plugin reports, which is the type the gateway actually acts on and
// the one the failure policy is read off.
//
// This is also what catches a new plugin whose blank import is missing from
// this file: its catalog entry resolves to no factory.
func TestCatalogTypeMatchesReportedType(t *testing.T) {
	for _, entry := range plugin.Builtins() {
		factory, ok := plugin.GetFactory(entry.Name)
		if !ok {
			t.Errorf("%s: catalog entry names no registered plugin — add its blank import to this file", entry.Name)
			continue
		}
		if reported := factory().Type(); reported != entry.Type {
			t.Errorf("%s: catalog says type %q, the plugin reports %q", entry.Name, entry.Type, reported)
		}
	}
}

// TestCatalogSettingsMatchTheKeysThePluginReads is the assertion that makes the
// catalog worth serving.
//
// It compares each entry's Settings against the keys its package actually reads
// out of the config map. Both directions matter: a key named here that no
// plugin reads is what shipped for four of six entries, and is invisible at
// runtime because a plugin ignores a config key it does not know; a key read but
// not named here is a setting an operator is never told about.
func TestCatalogSettingsMatchTheKeysThePluginReads(t *testing.T) {
	sources := pluginSources(t)

	for _, entry := range plugin.Builtins() {
		source, ok := sources[entry.Name]
		if !ok {
			t.Errorf("%s: no package under plugin/ registers this name", entry.Name)
			continue
		}

		read := make([]string, 0, len(entry.Settings))
		for _, match := range configKeyPattern.FindAllStringSubmatch(source, -1) {
			key := match[1]
			if slices.Contains(undocumentedKeys[entry.Name], key) || slices.Contains(read, key) {
				continue
			}
			read = append(read, key)
		}

		declared := slices.Clone(entry.Settings)
		slices.Sort(declared)
		slices.Sort(read)

		if !slices.Equal(declared, read) {
			t.Errorf("%s: catalog settings do not match the config keys the plugin reads\n"+
				"catalog: %v\n   Init: %v", entry.Name, declared, read)
		}
	}
}

// pluginSources maps each registered plugin name to the concatenated
// non-test source of the package that registers it.
func pluginSources(t *testing.T) map[string]string {
	t.Helper()

	dirs, err := filepath.Glob("*/*.go")
	if err != nil {
		t.Fatalf("glob plugin packages: %v", err)
	}

	byPackage := make(map[string]string)
	for _, path := range dirs {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		content, readErr := os.ReadFile(path) // #nosec G304 -- path comes from a fixed glob of this package's own tree.
		if readErr != nil {
			t.Fatalf("read %s: %v", path, readErr)
		}
		byPackage[filepath.Dir(path)] += string(content)
	}

	sources := make(map[string]string, len(byPackage))
	for _, source := range byPackage {
		for _, match := range registerPattern.FindAllStringSubmatch(source, -1) {
			sources[match[1]] = source
		}
	}
	return sources
}
