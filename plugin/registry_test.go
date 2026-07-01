package plugin

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
)

// registryMockPlugin is a test double for the Plugin interface.
type registryMockPlugin struct {
	name string
	typ  PluginType
}

func (m *registryMockPlugin) Name() string                                { return m.name }
func (m *registryMockPlugin) Type() PluginType                            { return m.typ }
func (m *registryMockPlugin) Init(_ map[string]any) error                 { return nil }
func (m *registryMockPlugin) Execute(_ context.Context, _ *Context) error { return nil }
func (m *registryMockPlugin) Close() error                                { return nil }

// unregisterFactory removes names from the registry under the write lock,
// matching the locked teardown in TestRegistryConcurrentAccess. Direct
// map deletes without the lock would race with concurrent RegisterFactory
// calls (e.g. from plugin init() on other goroutines).
func unregisterFactory(names ...string) {
	registryMu.Lock()
	defer registryMu.Unlock()
	for _, name := range names {
		delete(pluginRegistry, name)
	}
}

func TestRegisterFactory(t *testing.T) {
	// Clean up after test.
	defer unregisterFactory("mock-plugin")

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

// TestRegistryConcurrentAccess exercises RegisterFactory, GetFactory, and
// RegisteredPlugins concurrently to prove the registry mutex prevents the
// data race / "concurrent map read and map write" panic. Run under -race.
func TestRegistryConcurrentAccess(t *testing.T) {
	const goroutines = 32

	keys := make([]string, goroutines)
	for i := range keys {
		keys[i] = fmt.Sprintf("concurrent-plugin-%d", i)
	}
	defer unregisterFactory(keys...)

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		name := keys[i]
		wg.Add(2)
		// Writer.
		go func() {
			defer wg.Done()
			RegisterFactory(name, func() Plugin {
				return &registryMockPlugin{name: name, typ: TypeGuardrail}
			})
		}()
		// Concurrent reader.
		go func() {
			defer wg.Done()
			_, _ = GetFactory(name)
			_ = RegisteredPlugins()
		}()
	}
	wg.Wait()

	// All writers committed; every key must now be present.
	for _, k := range keys {
		if _, ok := GetFactory(k); !ok {
			t.Errorf("expected factory %q to be registered after concurrent writes", k)
		}
	}
}

func TestRegisteredPlugins(t *testing.T) {
	// Clean up after test.
	defer unregisterFactory("plugin-a", "plugin-b")

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

// TestRegisterFactory_Panics asserts RegisterFactory rejects an empty name,
// a nil factory, and duplicate registration, mirroring the guards in
// observability.RegisterExporter.
func TestRegisterFactory_Panics(t *testing.T) {
	validFactory := func() Plugin {
		return &registryMockPlugin{name: "panic-plugin", typ: TypeGuardrail}
	}

	t.Run("empty name", func(t *testing.T) {
		defer mustPanic(t)
		RegisterFactory("", validFactory)
	})

	t.Run("nil factory", func(t *testing.T) {
		defer mustPanic(t)
		RegisterFactory("panic-plugin-nil", nil)
	})

	t.Run("duplicate name", func(t *testing.T) {
		defer unregisterFactory("panic-plugin-dup")
		RegisterFactory("panic-plugin-dup", validFactory)

		defer mustPanic(t)
		RegisterFactory("panic-plugin-dup", validFactory)
	})
}

// mustPanic fails the test if the deferred call did not panic.
func mustPanic(t *testing.T) {
	t.Helper()
	if r := recover(); r == nil {
		t.Error("expected RegisterFactory to panic, but it did not")
	}
}
