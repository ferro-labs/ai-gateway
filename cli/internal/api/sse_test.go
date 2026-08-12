package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// frame wraps one JSON payload in the gateway's framing: `data: <json>\n\n`,
// no event name.
func frame(payload string) string { return "data: " + payload + "\n\n" }

const doneFrame = "data: [DONE]\n\n"

// writeSSE writes frames verbatim, flushing between them, so the client reads
// them as a stream rather than in one buffered shot.
func writeSSE(w http.ResponseWriter, requestID string, frames ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Request-ID", requestID)
	w.WriteHeader(http.StatusOK)
	f, ok := w.(http.Flusher)
	for _, fr := range frames {
		// A failed frame write means the client hung up mid-stream, which the
		// cancellation and early-close tests do on purpose. It is discarded
		// rather than reported: failing the test here would make those cases
		// flaky, and the client-side assertions already cover what was read.
		_, _ = io.WriteString(w, fr)
		if ok {
			f.Flush()
		}
	}
}

// drain reads until the channel closes, failing rather than hanging forever if
// it never does.
func drain(t *testing.T, st *ChatStream) []StreamEvent {
	t.Helper()
	var got []StreamEvent
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, open := <-st.Events:
			if !open {
				return got
			}
			got = append(got, ev)
		case <-deadline:
			t.Fatal("stream never closed its Events channel")
		}
	}
}

func content(events []StreamEvent) string {
	var b strings.Builder
	for _, ev := range events {
		if ev.Chunk == nil {
			continue
		}
		for _, ch := range ev.Chunk.Choices {
			b.WriteString(ch.Delta.Content)
		}
	}
	return b.String()
}

func TestStreamChatHappyPath(t *testing.T) {
	var gotBody, gotAccept, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotBody, gotAccept, gotAuth = string(raw), r.Header.Get("Accept"), r.Header.Get("Authorization")
		writeSSE(w, "trace-abc",
			frame(`{"id":"c1","object":"chat.completion.chunk","created":100,"model":"m","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`),
			frame(`{"id":"c1","object":"chat.completion.chunk","created":100,"model":"m","choices":[{"index":0,"delta":{"content":"Hello "},"finish_reason":null}]}`),
			frame(`{"id":"c1","object":"chat.completion.chunk","created":100,"model":"m","choices":[{"index":0,"delta":{"content":"world"},"finish_reason":"stop"}]}`),
			frame(`{"id":"c1","object":"chat.completion.chunk","created":100,"model":"m","choices":[],"usage":{"prompt_tokens":9,"completion_tokens":3,"total_tokens":12}}`),
			doneFrame,
		)
	}))
	defer srv.Close()

	c, _ := New(srv.URL, "fgw_secret")
	st, err := c.StreamChat(context.Background(), ChatRequest{
		Model:    "m",
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	defer st.Cancel()

	events := drain(t, st)
	if len(events) != 5 {
		t.Fatalf("want 4 chunks + Done, got %d events: %+v", len(events), events)
	}
	for i, ev := range events[:4] {
		if ev.Chunk == nil || ev.Err != nil || ev.Done {
			t.Fatalf("event %d must be a chunk, got %+v", i, ev)
		}
	}
	if !events[4].Done || events[4].Chunk != nil || events[4].Err != nil {
		t.Fatalf("last event must be Done, got %+v", events[4])
	}
	if got := content(events); got != "Hello world" {
		t.Fatalf("content = %q, want %q", got, "Hello world")
	}
	if fr := events[2].Chunk.Choices[0].FinishReason; fr == nil || *fr != "stop" {
		t.Fatalf("finish_reason not decoded: %v", fr)
	}
	usage := events[3].Chunk.Usage
	if usage == nil || usage.TotalTokens != 12 || usage.PromptTokens != 9 {
		t.Fatalf("usage chunk not decoded: %+v", usage)
	}
	if len(events[3].Chunk.Choices) != 0 {
		t.Fatalf("usage chunk carries an empty choices array, got %+v", events[3].Chunk.Choices)
	}
	if st.RequestID != "trace-abc" {
		t.Fatalf("RequestID = %q, want the X-Request-ID header", st.RequestID)
	}
	// The request itself: stream is forced on, and the SSE surface is asked for.
	if !strings.Contains(gotBody, `"stream":true`) {
		t.Fatalf("StreamChat must force stream:true, body was %s", gotBody)
	}
	if gotAccept != "text/event-stream" || gotAuth != "Bearer fgw_secret" {
		t.Fatalf("accept=%q auth=%q", gotAccept, gotAuth)
	}
}

func TestStreamChatDoneIgnoresFollowingBufferedFrames(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeSSE(w, "trace-done", doneFrame,
			frame(`{"id":"late","choices":[{"index":0,"delta":{"content":"must be ignored"}}]}`))
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "")
	st, err := c.StreamChat(context.Background(), ChatRequest{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Cancel()
	events := drain(t, st)
	if len(events) != 1 || !events[0].Done {
		t.Fatalf("[DONE] must terminate immediately: %+v", events)
	}
}

func TestStreamChatErrorFrameEndsStreamWithoutDone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeSSE(w, "trace-err",
			frame(`{"id":"c1","choices":[{"index":0,"delta":{"role":"assistant"}}]}`),
			frame(`{"error":{"message":"boom","type":"stream_error","code":"stream_error"}}`),
			// No [DONE]: that asymmetry is the gateway's real behavior.
		)
	}))
	defer srv.Close()

	c, _ := New(srv.URL, "")
	st, err := c.StreamChat(context.Background(), ChatRequest{Model: "m"})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	defer st.Cancel()

	events := drain(t, st)
	var errs, dones int
	for _, ev := range events {
		if ev.Err != nil {
			errs++
		}
		if ev.Done {
			dones++
		}
	}
	if errs != 1 || dones != 0 {
		t.Fatalf("want exactly one Err and no Done, got %d/%d: %+v", errs, dones, events)
	}
	last := events[len(events)-1]
	if last.Err.Code != CodeStreamError || last.Err.Message != "boom" || last.Err.Type != "stream_error" {
		t.Fatalf("error frame not decoded: %+v", last.Err)
	}
	if !strings.Contains(last.Err.Error(), "boom") {
		t.Fatalf("api.Error must read usefully: %q", last.Err.Error())
	}
}

func TestStreamChatToleratesCRLFAndComments(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeSSE(w, "trace-crlf",
			": keep-alive\r\n\r\n",
			"data: "+`{"id":"c1","choices":[{"index":0,"delta":{"content":"ok"}}]}`+"\r\n\r\n",
			// The space after the colon is optional in the SSE grammar. The
			// gateway always writes one; a proxy that re-frames the stream
			// need not, and dropping the frame would lose content silently.
			"data:"+`{"id":"c1","choices":[{"index":0,"delta":{"content":"!"}}]}`+"\r\n\r\n",
			"\r\n",
			"data: [DONE]\r\n\r\n",
		)
	}))
	defer srv.Close()

	c, _ := New(srv.URL, "")
	st, err := c.StreamChat(context.Background(), ChatRequest{Model: "m"})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	defer st.Cancel()

	events := drain(t, st)
	if got := content(events); got != "ok!" {
		t.Fatalf("content = %q, want %q (events %+v)", got, "ok!", events)
	}
	if !events[len(events)-1].Done {
		t.Fatalf("a CRLF [DONE] must terminate the stream: %+v", events)
	}
}

func TestStreamChatSkipsMalformedFrame(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeSSE(w, "trace-junk",
			frame(`{"id":"c1","choices":[{"index":0,"delta":{"content":"a"}}]}`),
			frame(`{"id":"c1","choices":[{"index":0,"delta":{"content":`), // truncated JSON
			frame(`{"id":"c1","choices":[{"index":0,"delta":{"content":"b"}}]}`),
			doneFrame,
		)
	}))
	defer srv.Close()

	c, _ := New(srv.URL, "")
	st, err := c.StreamChat(context.Background(), ChatRequest{Model: "m"})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	defer st.Cancel()

	events := drain(t, st)
	if got := content(events); got != "ab" {
		t.Fatalf("one bad frame must not kill the stream: content = %q, events %+v", got, events)
	}
	if !events[len(events)-1].Done {
		t.Fatalf("stream must still reach Done: %+v", events)
	}
}

func TestStreamChatTruncatedStreamReportsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// A well-formed frame, then the connection ends: no [DONE], no error
		// frame. A silently closed channel would render as a complete answer.
		writeSSE(w, "trace-cut", frame(`{"id":"c1","choices":[{"index":0,"delta":{"content":"half"}}]}`))
	}))
	defer srv.Close()

	c, _ := New(srv.URL, "")
	st, err := c.StreamChat(context.Background(), ChatRequest{Model: "m"})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	defer st.Cancel()

	events := drain(t, st)
	last := events[len(events)-1]
	if last.Err == nil || last.Err.Code != CodeStreamIncomplete {
		t.Fatalf("a truncated stream must surface an Err, got %+v", events)
	}
}

func TestStreamChatOversizedFrameNamesItsOwnCause(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeSSE(w, "trace-big", frame(`{"id":"c1","choices":[{"index":0,"delta":{"content":"`+
			strings.Repeat("x", maxStreamFrameBytes)+`"}}]}`))
	}))
	defer srv.Close()

	c, _ := New(srv.URL, "")
	st, err := c.StreamChat(context.Background(), ChatRequest{Model: "m"})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	defer st.Cancel()

	last := drain(t, st)
	e := last[len(last)-1].Err
	if e == nil || e.Code != CodeStreamIncomplete {
		t.Fatalf("an oversized frame still ends the stream incomplete: %+v", last)
	}
	// The stream is genuinely incomplete, so the code is right — but the
	// message must name the frame size, not a scanner internal, or the reader
	// goes looking at the network for a limit the client applied.
	if !strings.Contains(e.Message, "exceeded") || strings.Contains(e.Message, "token too long") {
		t.Fatalf("the message must name the frame limit, got %q", e.Message)
	}
}

func TestStreamChatSilentConnectionTimesOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	defer srv.Close()

	c, err := New(srv.URL, "", WithStreamIdleTimeout(25*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	st, err := c.StreamChat(context.Background(), ChatRequest{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Cancel()
	events := drain(t, st)
	if len(events) != 1 || events[0].Err == nil || events[0].Err.Code != CodeStreamTimeout {
		t.Fatalf("silent connection must end with stream_timeout: %+v", events)
	}
}

func TestStreamChatCancelMidStreamDoesNotDeadlock(t *testing.T) {
	released := make(chan struct{})
	tick := frame(`{"id":"c1","choices":[{"index":0,"delta":{"content":"tick"}}]}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(released)
		// Several frames, so the reader goroutine is blocked handing over a
		// later one — an unbuffered channel with a reader that walked away —
		// at the moment Cancel lands.
		writeSSE(w, "trace-cancel", tick, tick, tick)
		<-r.Context().Done() // never sends [DONE]: the client hangs up first
	}))
	defer srv.Close()

	c, _ := New(srv.URL, "")
	st, err := c.StreamChat(context.Background(), ChatRequest{Model: "m"})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}

	select {
	case ev := <-st.Events:
		if ev.Chunk == nil {
			t.Fatalf("want the first chunk, got %+v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no first chunk")
	}

	st.Cancel()
	drain(t, st) // must close promptly, with no sender left blocked
	st.Cancel()  // idempotent: always safe to call again
	<-released   // the server saw the hang-up, so the connection is not leaked

	// This asserts that the channel closes, not that no goroutine leaked: leak
	// detection under httptest means goroutine counting, which is flaky. Add it
	// if a leak is ever suspected.
}

func TestStreamChatPreStreamErrorReturnsAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusUnauthorized,
			`{"error":{"message":"bad key","type":"authentication_error","code":"unauthorized"}}`)
	}))
	defer srv.Close()

	c, _ := New(srv.URL, "fgw_bad")
	st, err := c.StreamChat(context.Background(), ChatRequest{Model: "m"})
	if st != nil {
		t.Fatal("a pre-stream failure must return no ChatStream")
	}
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized || apiErr.Message != "bad key" {
		t.Fatalf("want a decoded 401 envelope, got %v", err)
	}
}

func TestStreamChatURLMatchesDoResolutionForPathPrefixedBase(t *testing.T) {
	// StreamChat builds its URL through the same c.resolveURL join c.do uses
	// (see client.go), so a path-prefixed --gateway-url cannot land the
	// request on a different path than every other endpoint — in particular
	// it cannot produce a double slash on the streaming path.
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		writeSSE(w, "trace-prefix", doneFrame)
	}))
	defer srv.Close()

	for _, suffix := range []string{"/ferro/", "/ferro"} {
		c, err := New(srv.URL+suffix, "")
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		st, err := c.StreamChat(context.Background(), ChatRequest{Model: "m"})
		if err != nil {
			t.Fatalf("StreamChat: %v", err)
		}
		drain(t, st)
		st.Cancel()

		want := "/ferro/v1/chat/completions"
		if gotPath != want {
			t.Fatalf("base %q: request path = %q, want %q (no double slash)", srv.URL+suffix, gotPath, want)
		}
		if strings.Contains(gotPath, "//") {
			t.Fatalf("base %q: request path %q contains a double slash", srv.URL+suffix, gotPath)
		}
	}
}

func TestTraceAttribution(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/admin/logs" {
			t.Errorf("attribution must read /admin/logs, got %s", r.URL.Path)
		}
		writeJSON(t, w, http.StatusOK, `{"data":[
			{"trace_id":"other","provider":"openai","cost_usd":9.99},
			{"trace_id":"trace-abc","stage":"after_request","model":"m","provider":"anthropic","total_tokens":12,"cost_usd":0.0012}
		],"summary":{"total_entries":2,"returned_entries":2}}`)
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "fgw_k")

	row, err := c.TraceAttribution(context.Background(), "trace-abc", time.Time{})
	if err != nil {
		t.Fatalf("TraceAttribution: %v", err)
	}
	if row == nil || row.Provider != "anthropic" || row.CostUSD == nil || *row.CostUSD != 0.0012 {
		t.Fatalf("want the matching row, got %+v", row)
	}

	row, err = c.TraceAttribution(context.Background(), "not-in-the-page", time.Time{})
	if row != nil || err != nil {
		t.Fatalf("a missing row is (nil, nil) — log writes lag: got %+v, %v", row, err)
	}

	before := calls
	if row, err = c.TraceAttribution(context.Background(), "", time.Time{}); row != nil || err != nil || calls != before {
		t.Fatalf("an empty trace id must answer (nil, nil) without a request: %+v %v calls=%d", row, err, calls-before)
	}
}

func TestTraceAttributionNoLogStoreIsNotAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusNotImplemented,
			`{"error":{"message":"request logging is not configured","type":"server_error","code":"not_implemented"}}`)
	}))
	defer srv.Close()

	c, _ := New(srv.URL, "fgw_k")
	row, err := c.TraceAttribution(context.Background(), "trace-abc", time.Now().Add(-time.Minute))
	if row != nil || err != nil {
		t.Fatalf("a 501 log store must degrade to (nil, nil), got %+v, %v", row, err)
	}
}
