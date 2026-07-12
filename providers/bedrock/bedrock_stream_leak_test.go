package bedrock

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/ferro-labs/ai-gateway/providers/core"
	"go.uber.org/goleak"
)

// slowBedrockClient streams event chunks at a fixed cadence and stops feeding
// when the returned stream is Closed — mirroring how the real SDK aborts an
// event stream. It lets a leak test cancel mid-stream while the producer is
// still blocked forwarding a decoded chunk.
type slowBedrockClient struct {
	chunks [][]byte
	delay  time.Duration
}

func (slowBedrockClient) InvokeModel(context.Context, *bedrockruntime.InvokeModelInput, ...func(*bedrockruntime.Options)) (*bedrockruntime.InvokeModelOutput, error) {
	return nil, errors.New("unused")
}

func (c slowBedrockClient) InvokeModelWithResponseStream(_ context.Context, _ *bedrockruntime.InvokeModelWithResponseStreamInput, _ ...func(*bedrockruntime.Options)) (bedrockEventStream, error) {
	s := &slowEventStream{events: make(chan types.ResponseStream), done: make(chan struct{})}
	go func() {
		defer close(s.events)
		for _, b := range c.chunks {
			select {
			case s.events <- &types.ResponseStreamMemberChunk{Value: types.PayloadPart{Bytes: b}}:
			case <-s.done:
				return
			}
			select {
			case <-time.After(c.delay):
			case <-s.done:
				return
			}
		}
	}()
	return s, nil
}

type slowEventStream struct {
	events chan types.ResponseStream
	done   chan struct{}
	once   sync.Once
}

func (s *slowEventStream) Events() <-chan types.ResponseStream { return s.events }
func (s *slowEventStream) Close() error                        { s.once.Do(func() { close(s.done) }); return nil }
func (s *slowEventStream) Err() error                          { return nil }

// TestCompleteStream_ClientCancelMidStream_NoGoroutineLeak reproduces the
// direct-use failure mode: a slow Bedrock event stream and a consumer that
// reads one chunk, cancels the context, and stops reading. Before cancel-aware
// sends the producer blocked forever on `ch <- c` and never closed the event
// stream — leaking both the producer and the stream's feeder goroutine.
// goleak.VerifyNone asserts neither survives.
func TestCompleteStream_ClientCancelMidStream_NoGoroutineLeak(t *testing.T) {
	defer goleak.VerifyNone(t)

	frame := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`)
	chunks := make([][]byte, 100)
	for i := range chunks {
		chunks[i] = frame
	}
	p := &Provider{name: Name, client: slowBedrockClient{chunks: chunks, delay: 5 * time.Millisecond}}

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := p.CompleteStream(ctx, core.Request{
		Model:    "anthropic.claude-3-5-sonnet-20241022-v2:0",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	})
	if err != nil {
		cancel()
		t.Fatalf("CompleteStream: %v", err)
	}

	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("no first chunk within 2s")
	}

	// Client disconnects: cancel and stop reading. The producer must abandon its
	// pending send, close the event stream (stopping the feeder), and exit.
	cancel()
}
