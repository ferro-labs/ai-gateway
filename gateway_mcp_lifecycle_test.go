package aigateway

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/mcp"
)

// R8: closeOnce has already fired by the time a late reload lands, so a registry
// built after shutdown would spawn its subprocesses and leave nothing able to
// close them — leaked for the life of the host process.
func TestReloadConfigAfterCloseIsRejected(t *testing.T) {
	gw, err := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "openai"}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := gw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	err = gw.ReloadConfig(context.Background(), Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "openai"}},
		MCPServers: []mcp.ServerConfig{{
			Name: "late", URL: "http://127.0.0.1:1/mcp",
		}},
	})
	if err == nil {
		t.Fatal("ReloadConfig after Close succeeded; it would spawn MCP subprocesses nothing can ever close")
	}
}

// R5: ReloadConfig writes g.mcpRegistry under g.mu while Close used to read it
// after unlocking. Race-detector reproducible, and the losing interleaving left
// a live registry unclosed. Run this with -race.
func TestCloseAndReloadConfigDoNotRace(t *testing.T) {
	mcpSrv := newMCPTestServer(t)
	defer mcpSrv.Close()

	base := Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "openai"}},
		MCPServers: []mcp.ServerConfig{{
			Name: "s1", URL: mcpSrv.URL, TimeoutSeconds: 5,
		}},
	}

	gw, err := New(base)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	select {
	case <-gw.MCPInitDone():
	case <-time.After(10 * time.Second):
		t.Fatal("MCP init timeout")
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		// A late reload may legitimately be rejected once Close has won the
		// race; both outcomes are correct, neither may race or leak.
		_ = gw.ReloadConfig(context.Background(), base)
	}()
	go func() {
		defer wg.Done()
		_ = gw.Close()
	}()
	wg.Wait()
}
