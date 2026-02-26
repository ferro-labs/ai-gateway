package plugin

import (
	"context"
	"sort"
	"testing"
)

// registryMockPlugin is a test double for the Plugin interface.
type registryMockPlugin struct {
	name string
	typ  PluginType
}

func (m *registryMockPlugin) Name() string                                { return m.name }
func (m *registryMockPlugin) Type() PluginType                            { return m.typ }
func (m *registryMockPlugin) Init(_ map[string]interface{}) error    { return nil }
func (m *registryMockPlugin) Execute(_ context.Context, _ *Context) error { return nil }

func TestRegisterFactory(t *testing.T) {
	// Clean up after test.
	defer delete(pluginRegistry, "mock-plugin")

	RegisterFactory("mock-plugin", func() Plugin {
		return &registryMockPlugin{name: "mock-plugin", typ: TypeGuardrail}
	})

	f, ok := GetFactory("mock-plugin")
	if !ok {
		t.Fatal("expected factory to be registered")
	}

	p := f()
	if p.Name() != "mock-plugin" {
		t.Errorf("got name %q, want mock-plugin", p.Name())
	}
	if p.Type() != TypeGuardrail {
		t.Errorf("got type %q, want %q", p.Type(), TypeGuardrail)
	}
}

func TestGetFactory_NotFound(t *testing.T) {
	_, ok := GetFactory("nonexistent-plugin")
	if ok {
		t.Fatal("expected factory not to be found")
	}
}

func TestRegisteredPlugins(t *testing.T) {
	// Clean up after test.
	defer delete(pluginRegistry, "plugin-a")
	defer delete(pluginRegistry, "plugin-b")

	RegisterFactory("plugin-a", func() Plugin {
		return &registryMockPlugin{name: "plugin-a", typ: TypeGuardrail}
	})
	RegisterFactory("plugin-b", func() Plugin {
		return &registryMockPlugin{name: "plugin-b", typ: TypeLogging}
	})

	names := RegisteredPlugins()
	// Filter to only our test plugins since other tests/init may register plugins.
	var filtered []string
	for _, n := range names {
		if n == "plugin-a" || n == "plugin-b" {
			filtered = append(filtered, n)
		}
	}
	sort.Strings(filtered)

	if len(filtered) != 2 {
		t.Fatalf("expected 2 plugins, got %d: %v", len(filtered), filtered)
	}
	if filtered[0] != "plugin-a" || filtered[1] != "plugin-b" {
		t.Errorf("got %v, want [plugin-a plugin-b]", filtered)
	}
}
