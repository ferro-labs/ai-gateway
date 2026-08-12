package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ChatMessage is one turn of a chat request.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest is the subset of POST /v1/chat/completions ferro sends.
//
// Stream is forced true by StreamChat: the CLI has no non-streaming path. The
// cost is that a streamed body carries no provider — that is resolved from the
// request log instead, by TraceAttribution.
type ChatRequest struct {
	Model    string        `json:"model"`
	Messages []ChatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

// Usage is the token accounting the gateway sends in a chunk of its own before
// [DONE]. It arrives whether or not the client asked for it — the gateway
// strips usage only on an explicit stream_options.include_usage:false — so a
// stream that ends without one ended abnormally.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	ReasoningTokens  int `json:"reasoning_tokens,omitempty"`
	CacheReadTokens  int `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
}

// MessageDelta is the incremental piece of one choice.
//
// ToolCalls is carried raw and unrendered until v0.2: decoding into a typed
// shape ferro does not display would make a tool-calling stream fail to parse
// for no benefit, and dropping the field would make it unrecoverable.
type MessageDelta struct {
	Role             string          `json:"role,omitempty"`
	Content          string          `json:"content,omitempty"`
	ReasoningContent string          `json:"reasoning_content,omitempty"`
	ToolCalls        json.RawMessage `json:"tool_calls,omitempty"`
}

// StreamChoice is one candidate's delta within a chunk. FinishReason is a
// pointer because every chunk carries the field and only the last one carries
// a value.
type StreamChoice struct {
	Index        int          `json:"index"`
	Delta        MessageDelta `json:"delta"`
	FinishReason *string      `json:"finish_reason"`
}

// ChatChunk is one `data:` frame of a streamed completion. Choices may be
// empty — the usage chunk is exactly that shape — but the gateway never sends
// null for it.
type ChatChunk struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []StreamChoice `json:"choices"`
	Usage   *Usage         `json:"usage,omitempty"`
}

// StreamEvent is one thing that happened on a stream: exactly one of the three
// fields is set.
//
// Err and Done are both terminal and mutually exclusive. A stream that fails
// mid-flight ends with an error frame and NO [DONE], so a reader that waits for
// Done after an Err waits forever; read until the channel closes instead.
type StreamEvent struct {
	Chunk *ChatChunk // a decoded content/usage frame
	Err   *Error     // mid-stream failure — the stream is over
	Done  bool       // data: [DONE]
}

// ChatStream is a live streamed completion.
//
// RequestID is the X-Request-ID response header, which equals the trace_id of
// the matching /admin/logs row. It is the only way to attribute a streamed
// request: no chunk names a provider.
//
// Cancel is always safe to call, safe to call more than once, and never blocks
// — including while the reader has stopped reading Events. Call it (defer it)
// even after a clean Done: it is what releases the reader goroutine and the
// connection.
type ChatStream struct {
	RequestID string
	Events    <-chan StreamEvent
	Cancel    context.CancelFunc
}

// Stream error codes. The first two are the gateway's own, sent inside an
// error frame; the third is ferro's, synthesized when the connection ends with
// neither a terminator nor an explanation.
const (
	CodeStreamError      = "stream_error"      // gateway: the upstream or the pipeline failed
	CodeStreamTimeout    = "stream_timeout"    // gateway: the idle bound elapsed
	CodeStreamIncomplete = "stream_incomplete" // client: connection ended without [DONE]

	// DefaultStreamIdleTimeout prevents a connected-but-silent upstream from
	// leaving a CLI command or console turn waiting forever.
	DefaultStreamIdleTimeout = 2 * time.Minute

	// maxStreamFrameBytes bounds one SSE frame, which is one line. It is a
	// memory bound on a stream nothing else limits, so it stays where it is:
	// raising it only moves the wall, and a frame this size is a broken or
	// hostile upstream rather than an answer.
	maxStreamFrameBytes = 1 << 20
)

type scanResult struct {
	line string
	err  error
	done bool
}

// StreamChat opens a streamed chat completion.
//
// A non-2xx arriving before the stream starts is returned as an *Error and
// no ChatStream. Once streaming has begun every failure is delivered on the
// channel instead, because the HTTP status is already 200 by then.
//
// The caller owns the returned stream's lifetime: read Events until it closes,
// and call Cancel.
func (c *Client) StreamChat(ctx context.Context, req ChatRequest) (*ChatStream, error) {
	req.Stream = true // a value copy: the caller's request is not mutated
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	// resolveURL is the same join c.do uses for every other endpoint, so a
	// path-prefixed --gateway-url (e.g. https://gw.example.com/ferro) cannot
	// resolve to a different URL here than it would through do.
	reqURL := c.resolveURL("/v1/chat/completions").String()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(body))
	if err != nil {
		cancel()
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("User-Agent", c.userAgent)
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	// A dedicated client: the shared one carries DefaultTimeout, which would
	// cut every stream off after 15s. Lifetime is ctx's job here. The redirect
	// refusal is not optional — a bearer token must not replay to whatever
	// host a redirect names, on this surface as much as any other.
	streamClient := &http.Client{
		Transport:     c.hc.Transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := streamClient.Do(httpReq)
	if err != nil {
		cancel()
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		defer cancel()
		if resp.StatusCode < 400 {
			return nil, &Error{
				Status:     resp.StatusCode,
				Message:    "redirect refused",
				RedirectTo: resp.Header.Get("Location"),
			}
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		return nil, decodeAPIError(resp.StatusCode, raw)
	}

	events := make(chan StreamEvent)
	st := &ChatStream{RequestID: resp.Header.Get("X-Request-ID"), Events: events, Cancel: cancel}

	go func() {
		defer close(events)
		defer cancel()
		defer func() { _ = resp.Body.Close() }()

		lines := make(chan scanResult)
		go scanStream(ctx, resp.Body, lines)

		var idle <-chan time.Time
		var timer *time.Timer
		if c.streamIdleTimeout > 0 {
			timer = time.NewTimer(c.streamIdleTimeout)
			idle = timer.C
			defer timer.Stop()
		}
		resetIdle := func() {
			if timer == nil {
				return
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(c.streamIdleTimeout)
		}

		for {
			select {
			case <-ctx.Done():
				return
			case <-idle:
				sendEvent(ctx, events, StreamEvent{Err: &Error{
					Status: http.StatusGatewayTimeout, Type: CodeStreamError, Code: CodeStreamTimeout,
					Message: "stream produced no events before the idle timeout",
				}})
				cancel()
				return
			case result := <-lines:
				if result.done {
					if ctx.Err() != nil {
						return
					}
					msg := "stream ended without a terminating frame"
					switch {
					case errors.Is(result.err, bufio.ErrTooLong):
						// The stream really is incomplete, so the code is right,
						// but "read failed: token too long" names a Go type and
						// sends the reader looking at the network. Say what the
						// gateway sent instead.
						msg = fmt.Sprintf("a single stream frame exceeded the %d-byte limit; the answer is truncated", maxStreamFrameBytes)
					case result.err != nil:
						msg = "stream read failed: " + result.err.Error()
					}
					sendEvent(ctx, events, StreamEvent{Err: &Error{
						Status: http.StatusBadGateway, Type: CodeStreamError, Code: CodeStreamIncomplete, Message: msg,
					}})
					return
				}
				if strings.TrimSpace(result.line) != "" {
					resetIdle()
				}
				line := result.line
				if !strings.HasPrefix(line, "data:") {
					continue // blank keep-alives, ": comments", and any field ferro does not read
				}
				// TrimSpace absorbs both the optional space after the colon and
				// the \r of a CRLF stream.
				payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))

				if payload == "[DONE]" {
					sendEvent(ctx, events, StreamEvent{Done: true})
					return
				}
				if e := decodeErrorFrame(payload); e != nil {
					// An error frame is the last frame: no [DONE] follows it, so
					// this goroutine must not go back to waiting for one.
					sendEvent(ctx, events, StreamEvent{Err: e})
					return
				}
				var chunk ChatChunk
				if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
					continue // one malformed frame never kills a live stream
				}
				if !sendEvent(ctx, events, StreamEvent{Chunk: &chunk}) {
					return
				}
			}
		}
	}()
	return st, nil
}

func scanStream(ctx context.Context, r io.Reader, out chan<- scanResult) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxStreamFrameBytes)
	for sc.Scan() {
		select {
		case out <- scanResult{line: sc.Text()}:
		case <-ctx.Done():
			return
		}
	}
	select {
	case out <- scanResult{err: sc.Err(), done: true}:
	case <-ctx.Done():
	}
}

// decodeErrorFrame reports whether payload is a mid-stream error frame, and
// decodes it. A content chunk carries no "error" key, so this cannot false-fire
// on one.
func decodeErrorFrame(payload string) *Error {
	var probe struct {
		Error *struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(payload), &probe) != nil || probe.Error == nil {
		return nil
	}
	// 502: the HTTP response was already 200 by the time this arrived, so the
	// status is synthesized to say "the upstream failed mid-answer".
	return &Error{
		Status:  http.StatusBadGateway,
		Message: probe.Error.Message,
		Type:    probe.Error.Type,
		Code:    probe.Error.Code,
	}
}

// sendEvent delivers ev unless ctx is cancelled first, and reports whether it
// was delivered. Every send goes through it: an unbuffered channel plus a
// reader that stopped reading is exactly the deadlock Cancel exists to break.
func sendEvent(ctx context.Context, ch chan<- StreamEvent, ev StreamEvent) bool {
	select {
	case ch <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}

// TraceAttribution resolves which provider served a request and what it cost,
// by matching a response's X-Request-ID against /admin/logs trace_id. Streamed
// chunks carry no provider, so this is the only attribution path.
//
// It answers (nil, nil) rather than an error for every "cannot know yet"
// case — no trace id, no matching row (the log write lags the response), or a
// gateway with no log store (501). Attribution is a detail shown beside an
// answer that already arrived; failing the command over its absence would turn
// a cosmetic gap into a broken chat.
func (c *Client) TraceAttribution(ctx context.Context, traceID string, since time.Time) (*LogEntry, error) {
	if traceID == "" {
		return nil, nil
	}
	page, err := c.Logs(ctx, LogsQuery{Limit: 50, Since: since})
	if err != nil {
		if IsNotSupported(err) {
			return nil, nil
		}
		return nil, err
	}
	for _, e := range page.Data {
		if e.TraceID == traceID {
			return &e, nil
		}
	}
	return nil, nil
}
