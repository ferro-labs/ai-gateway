package aigateway

import (
	"context"
	"sync"
	"testing"

	internalmcp "github.com/ferro-labs/ai-gateway/internal/mcp"
	"github.com/ferro-labs/ai-gateway/providers"
)

// freshMockProvider returns a new *providers.Response on every call, avoiding
// the shared-pointer data race that would occur if multiple goroutines mutate
// a single response returned by the default mockProvider.
type freshMockProvider struct {
	name   string
	models []string
}

func (p *freshMockProvider) Name() string                  { return p.name }
func (p *freshMockProvider) SupportedModels() []string     { return p.models }
func (p *freshMockProvider) Models() []providers.ModelInfo { return nil }
func (p *freshMockProvider) SupportsModel(model string) bool {
	for _, m := range p.models {
		if m == model {
			return true
		}
	}
	return false
}
func (p *freshMockProvider) Complete(_ context.Context, _ providers.Request) (*providers.Response, error) {
	return &providers.Response{
		ID:      "r1",
		Choices: []providers.Choice{{Message: providers.Message{Role: "assistant", Content: "ok"}}},
	}, nil
}

// TestRoute_MCPSnapshotIsolation verifies that Route uses the mcpRegistry /
// mcpExecutor values captured at request entry, not whatever the live fields
// contain later. Concretely: if the registry is nil when the snapshot is taken,
// no MCP tool injection occurs even if a registry is installed after the lock
// is released.
func TestRoute_MCPSnapshotIsolation(t *testing.T) {
	gw, err := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "mock"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	gw.RegisterProvider(&freshMockProvider{
		name:   "mock",
		models: []string{"gpt-4o"},
	})

	// mcpRegistry is nil at this point — snapshot taken inside Route will be nil.
	resp, err := gw.Route(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Route() error: %v", err)
	}
	if len(resp.Choices) == 0 {
		t.Fatal("expected at least one choice")
	}
	// No MCP tools should have been injected (registry was nil at snapshot time).
	if len(resp.Choices[0].Message.ToolCalls) != 0 {
		t.Errorf("unexpected tool calls: %v", resp.Choices[0].Message.ToolCalls)
	}
}

// TestRoute_MCPFieldsSnapshotRace verifies that concurrent writes to
// g.mcpRegistry / g.mcpExecutor (under g.mu.Lock) do not race with Route's
// reads of those fields.
//
// Before the fix, Route read g.mcpRegistry and g.mcpExecutor after releasing
// g.mu, so a concurrent writer could replace the pointers mid-flight.  The
// fix snapshots both fields inside the initial g.mu.RLock block.
//
// Run with: go test -race -run TestRoute_MCPFieldsSnapshotRace .
func TestRoute_MCPFieldsSnapshotRace(t *testing.T) {
	gw, err := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "mock"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	gw.RegisterProvider(&freshMockProvider{
		name:   "mock",
		models: []string{"gpt-4o"},
	})

	req := providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	}

	var wg sync.WaitGroup
	const iters = 300

	// Writer: toggles mcpRegistry/mcpExecutor under the gateway lock, exactly
	// as ReloadConfig does (but without touching any other field, so we isolate
	// only the MCP snapshot race).
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			gw.mu.Lock()
			reg := internalmcp.NewRegistry()
			gw.mcpRegistry = reg
			gw.mcpExecutor = internalmcp.NewExecutor(reg, 5, nil)
			gw.mu.Unlock()

			gw.mu.Lock()
			gw.mcpRegistry = nil
			gw.mcpExecutor = nil
			gw.mu.Unlock()
		}
	}()

	// Readers: call Route concurrently with the writer.
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				_, _ = gw.Route(context.Background(), req)
			}
		}()
	}

	wg.Wait()
}

// TestRouteStream_MCPRegistrySnapshotRace verifies that RouteStream's
// mcpRegistry snapshot (taken inside the initial g.mu.RLock block) does not
// race with concurrent writes to g.mcpRegistry.
//
// The old code acquired a *second* g.mu.RLock just for the hasMCP check; the
// fix collapses it into the first lock block so the field is read exactly once,
// under the same lock that protects all other request-entry snapshots.
func TestRouteStream_MCPRegistrySnapshotRace(t *testing.T) {
	ch := make(chan providers.StreamChunk)
	close(ch)

	gw, err := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "mock-stream"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{
			name:   "mock-stream",
			models: []string{"gpt-4o"},
		},
		streamCh: ch,
	})

	req := providers.Request{
		Model:    "gpt-4o",
		Stream:   true,
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	}

	var wg sync.WaitGroup
	const iters = 300

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			gw.mu.Lock()
			reg := internalmcp.NewRegistry()
			gw.mcpRegistry = reg
			gw.mcpExecutor = internalmcp.NewExecutor(reg, 5, nil)
			gw.mu.Unlock()

			gw.mu.Lock()
			gw.mcpRegistry = nil
			gw.mcpExecutor = nil
			gw.mu.Unlock()
		}
	}()

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				out, err := gw.RouteStream(context.Background(), req)
				if err != nil {
					continue
				}
				for range out { //nolint:revive
				}
			}
		}()
	}

	wg.Wait()
}
