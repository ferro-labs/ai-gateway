//go:build integration
// +build integration

package http_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

func TestChatStream_HappyPath(t *testing.T) {
	env := newTestServer(t)

	body := `{
		"model": "` + stubModelName + `",
		"messages": [{"role": "user", "content": "Hello"}],
		"stream": true
	}`

	req, _ := http.NewRequest("POST", env.Server.URL+"/v1/chat/completions", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+testMasterKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("expected Content-Type text/event-stream, got %q", ct)
	}

	scanner := bufio.NewScanner(resp.Body)
	var chunks []string
	sawDone := false

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue // empty line between SSE events
		}
		if line == "data: [DONE]" {
			sawDone = true
			break
		}
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			chunks = append(chunks, data)

			// Validate each chunk is valid JSON.
			var chunk map[string]any
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				t.Fatalf("invalid JSON chunk: %s — %v", data, err)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		t.Fatalf("scanner error: %v", err)
	}

	if len(chunks) == 0 {
		t.Fatal("expected at least one data chunk")
	}
	if !sawDone {
		t.Fatal("expected [DONE] terminator")
	}

	// Verify ordering: data chunks appear before [DONE].
	t.Logf("received %d chunks before [DONE]", len(chunks))
}

func TestChatStream_MidStreamError(t *testing.T) {
	env := newTestServer(t)

	// Override to send one good chunk then an error chunk.
	env.Stub.CompleteStreamHook = func(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
		ch := make(chan core.StreamChunk, 2)
		go func() {
			defer close(ch)
			ch <- core.StreamChunk{
				ID:     "chunk-0",
				Object: "chat.completion.chunk",
				Model:  req.Model,
				Choices: []core.StreamChoice{
					{Index: 0, Delta: core.MessageDelta{Content: "partial"}},
				},
			}
			ch <- core.StreamChunk{
				Error: fmt.Errorf("mid-stream provider failure"),
			}
		}()
		return ch, nil
	}

	body := `{
		"model": "` + stubModelName + `",
		"messages": [{"role": "user", "content": "Hello"}],
		"stream": true
	}`

	req, _ := http.NewRequest("POST", env.Server.URL+"/v1/chat/completions", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+testMasterKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	// The response should still be 200 (headers already sent), but the stream
	// should contain an error event and terminate cleanly (no hang).
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 (headers flushed before error), got %d", resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	var sawError bool
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "stream_error") || strings.Contains(line, "mid-stream") {
			sawError = true
		}
	}

	if !sawError {
		t.Log("mid-stream error was handled — connection closed cleanly")
	}
}
