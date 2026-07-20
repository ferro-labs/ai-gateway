package aigateway

import (
	"context"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/mcp"
	providers "github.com/ferro-labs/ai-gateway/providers"
)

// chunkStreamProvider emits several chunks so a test can tell real streaming
// from the MCP redirect, which collapses the whole response into one chunk.
type chunkStreamProvider struct {
	mockProvider
	chunks int
}

func (c *chunkStreamProvider) CompleteStream(ctx context.Context, _ providers.Request) (<-chan providers.StreamChunk, error) {
	ch := make(chan providers.StreamChunk, c.chunks)
	go func() {
		defer close(ch)
		for i := 0; i < c.chunks; i++ {
			select {
			case ch <- providers.StreamChunk{
				ID:    "chunk",
				Model: "gpt-4o",
				Choices: []providers.StreamChoice{{
					Delta: providers.MessageDelta{Content: "tok"},
				}},
			}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

// A configured-but-unreachable MCP server must not disable streaming.
//
// Regression guard for B1: RouteStream gated on Registry.HasServers(), which is
// true from the moment a server is *registered* — before, and regardless of,
// the handshake. One typo'd command or a dead URL therefore turned every
// stream:true request on the gateway into a fake single-chunk stream, forever,
// for every caller — including callers who never used MCP.
func TestGateway_RouteStream_BrokenMCPServerDoesNotDisableStreaming(t *testing.T) {
	const wantChunks = 3

	sp := &chunkStreamProvider{
		mockProvider: mockProvider{name: "mock-stream", models: []string{"gpt-4o"}},
		chunks:       wantChunks,
	}

	gw, err := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "mock-stream"}},
		MCPServers: []mcp.ServerConfig{{
			// Reserved discard port: registration succeeds, the handshake never does.
			Name:           "dead-mcp",
			URL:            "http://127.0.0.1:1/mcp",
			TimeoutSeconds: 1,
		}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.RegisterProvider(sp)

	// Let initialization run and fail before routing.
	select {
	case <-gw.MCPInitDone():
	case <-time.After(10 * time.Second):
		t.Fatal("MCP init timeout")
	}

	ch, err := gw.RouteStream(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Stream:   true,
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("RouteStream error: %v", err)
	}

	var got int
	for chunk := range ch {
		if chunk.Error != nil {
			t.Fatalf("stream chunk error: %v", chunk.Error)
		}
		got++
	}

	if got != wantChunks {
		t.Fatalf("got %d chunks, want %d — a dead MCP server collapsed the stream "+
			"(got 1 chunk means RouteStream took the MCP redirect)", got, wantChunks)
	}
}
