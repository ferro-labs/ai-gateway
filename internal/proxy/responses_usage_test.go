package proxy

import (
	"io"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
)

// drain reads the wrapped body fully (as the ReverseProxy copy would) and returns
// the bytes seen by the client plus the captured usage.
func drain(t *testing.T, raw string, stream bool) (string, providers.Usage) {
	t.Helper()
	var usage providers.Usage
	rc := newResponsesUsageReader(io.NopCloser(strings.NewReader(raw)), stream, &usage)
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := rc.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return string(got), usage
}

func TestResponsesUsageReader_NonStreamJSON(t *testing.T) {
	body := `{"id":"resp_1","object":"response","status":"completed","output":[{"type":"message"}],"usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":20},"output_tokens":50,"output_tokens_details":{"reasoning_tokens":10},"total_tokens":150}}`
	got, usage := drain(t, body, false)
	if got != body {
		t.Errorf("body altered:\n got %q\nwant %q", got, body)
	}
	if usage.PromptTokens != 100 || usage.CompletionTokens != 50 || usage.TotalTokens != 150 {
		t.Errorf("usage = %+v, want prompt=100 completion=50 total=150", usage)
	}
	if usage.CacheReadTokens != 20 || usage.ReasoningTokens != 10 {
		t.Errorf("usage details = cache=%d reasoning=%d, want 20/10", usage.CacheReadTokens, usage.ReasoningTokens)
	}
}

func TestResponsesUsageReader_StreamingTerminalEvent(t *testing.T) {
	// created (usage null) then a delta then the terminal completed event.
	stream := strings.Join([]string{
		`event: response.created`,
		`data: {"type":"response.created","response":{"id":"resp_1","status":"in_progress","usage":null}}`,
		``,
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","delta":"hello usage of tokens"}`,
		``,
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","usage":{"input_tokens":7,"output_tokens":3,"total_tokens":10,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}}}`,
		``,
		``,
	}, "\n")

	got, usage := drain(t, stream, true)
	if got != stream {
		t.Errorf("stream bytes altered")
	}
	if usage.PromptTokens != 7 || usage.CompletionTokens != 3 || usage.TotalTokens != 10 {
		t.Errorf("usage = %+v, want prompt=7 completion=3 total=10 (from response.completed)", usage)
	}
}

// A delta whose text contains the literal "usage" must not be mistaken for the
// terminal event — usage stays whatever the real terminal event set.
func TestResponsesUsageReader_DeltaMentioningUsageIsIgnored(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"the word usage appears here"}`,
		``,
		`data: {"type":"response.failed","response":{"status":"failed","usage":null,"error":{"message":"boom"}}}`,
		``,
	}, "\n")
	_, usage := drain(t, stream, true)
	// failed carries null usage → nothing priced.
	if usage.TotalTokens != 0 || usage.PromptTokens != 0 {
		t.Errorf("failed stream should leave usage zero, got %+v", usage)
	}
}

// Usage split across Read boundaries (the terminal frame arrives in two chunks)
// is still captured, because bytes accumulate into the line buffer until newline.
func TestResponsesUsageReader_SplitAcrossReads(t *testing.T) {
	full := "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":5,\"output_tokens\":5,\"total_tokens\":10,\"input_tokens_details\":{\"cached_tokens\":0},\"output_tokens_details\":{\"reasoning_tokens\":0}}}}\n\n"
	var usage providers.Usage
	rc := newResponsesUsageReader(io.NopCloser(&chunkedReader{data: full, chunk: 17}), true, &usage)
	if _, err := io.ReadAll(rc); err != nil {
		t.Fatalf("read: %v", err)
	}
	_ = rc.Close()
	if usage.TotalTokens != 10 {
		t.Errorf("usage total = %d, want 10 (terminal frame split across reads)", usage.TotalTokens)
	}
}

// chunkedReader returns data in small fixed-size chunks to force Read boundaries
// mid-frame.
type chunkedReader struct {
	data  string
	chunk int
	pos   int
}

func (c *chunkedReader) Read(p []byte) (int, error) {
	if c.pos >= len(c.data) {
		return 0, io.EOF
	}
	end := c.pos + c.chunk
	if end > len(c.data) {
		end = len(c.data)
	}
	n := copy(p, c.data[c.pos:end])
	c.pos += n
	return n, nil
}
