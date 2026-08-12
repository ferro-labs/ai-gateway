package command

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/ferro-labs/ai-gateway/cli/internal/api"
	"github.com/ferro-labs/ai-gateway/cli/internal/fixture"
)

// fullReply is what the fixture streams back, one delta per word. The command
// must reproduce it byte for byte: stdout is the answer, not a rendering of it.
const fullReply = "Ferro routed this through the fake gateway."

// recorder captures what ferro put on the wire, so a test can assert both what
// was sent and — for the flag-validation case — that nothing was sent at all.
type recorder struct {
	mu       sync.Mutex
	requests int
	chatBody []byte
}

func (r *recorder) record(path string, body []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests++
	if path == "/v1/chat/completions" {
		r.chatBody = body
	}
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.requests
}

func (r *recorder) chat() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.chatBody
}

// recordingGateway is the fixture with a tap on it. The fixture stays the one
// definition of the wire contract; this only observes the traffic.
func recordingGateway(t *testing.T, s fixture.State) (*httptest.Server, *recorder) {
	t.Helper()
	rec := &recorder{}
	inner := fixture.Handler(s)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		rec.record(r.URL.Path, body)
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

// runChatWith runs one invocation with control over the three things a chat
// cares about that the shared `run` helper hides: stdin, the stdout writer, and
// the context that an interrupt cancels.
func runChatWith(ctx context.Context, t *testing.T, srv *httptest.Server, stdin io.Reader, stdout io.Writer, args ...string) (stderr string, err error) {
	t.Helper()
	t.Setenv("FERRO_API_KEY", "fgw_test")
	root := NewRoot()
	var errb bytes.Buffer
	if stdin != nil {
		root.SetIn(stdin)
	}
	root.SetOut(stdout)
	root.SetErr(&errb)
	root.SetArgs(append(args, "--gateway-url", srv.URL))
	// Executed on its own line: `return errb.String(), root.Execute…` would read
	// the buffer before the command that fills it has run.
	err = root.ExecuteContext(ctx)
	return errb.String(), err
}

func TestChatStreamsContentToStdoutVerbatim(t *testing.T) {
	srv := gateway(t, fixture.Default())

	stdout, _, err := run(t, srv, "chat", "hello", "there", "--model", "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("a complete stream must exit 0: %v", err)
	}
	if stdout != fullReply+"\n" {
		t.Fatalf("stdout must carry the answer verbatim, got %q want %q", stdout, fullReply+"\n")
	}
	if strings.Contains(stdout, "\x1b") {
		t.Fatalf("stdout must be free of ANSI — it is the pipeable channel: %q", stdout)
	}
}

// The answer belongs on stdout; everything ferro knows *about* the answer
// belongs on stderr, or `ferro chat ... > answer.txt` captures the narrative too.
func TestChatMetaLineGoesToStderrNotStdout(t *testing.T) {
	srv := gateway(t, fixture.Default())

	stdout, stderr, err := run(t, srv, "chat", "hi", "--model", "claude-sonnet-4-6")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"claude-sonnet-4-6", "anthropic", "24 in / 7 out", "$0.0031"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("meta line missing %q, stderr was %q", want, stderr)
		}
	}
	if stdout != fullReply+"\n" {
		t.Fatalf("meta leaked into stdout: %q", stdout)
	}
}

func TestChatJSONIsExactlyOneDocument(t *testing.T) {
	srv := gateway(t, fixture.Default())

	stdout, _, err := run(t, srv, "chat", "hi", "--model", "claude-sonnet-4-6", "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	doc := oneJSONDoc(t, stdout)
	if doc["content"] != fullReply {
		t.Fatalf("json content must be the whole answer: %v", doc["content"])
	}
	for _, k := range []string{"model", "content", "usage", "request_id", "provider", "cost_usd"} {
		if _, ok := doc[k]; !ok {
			t.Fatalf("json document missing %q: %v", k, doc)
		}
	}
	if strings.Contains(stdout, fullReply+"\n"+fullReply) {
		t.Fatalf("json mode must not also stream prose: %q", stdout)
	}
}

// The model is not a detail the gateway can supply a default for, and a request
// missing it is a wasted round trip against a live gateway.
func TestChatWithoutModelFailsBeforeAnyRequest(t *testing.T) {
	srv, rec := recordingGateway(t, fixture.Default())

	_, err := runChatWith(context.Background(), t, srv, nil, io.Discard, "chat", "hi")
	if err == nil {
		t.Fatal("a chat with no --model must fail")
	}
	if !strings.Contains(err.Error(), "model") {
		t.Fatalf("the error must name the missing flag, got %v", err)
	}
	if n := rec.count(); n != 0 {
		t.Fatalf("nothing may reach the gateway before --model is validated, saw %d requests", n)
	}
}

// An empty value passes cobra's required-flag check (the flag *was* set), so
// the guard has to be ferro's own.
func TestChatEmptyModelFailsBeforeAnyRequest(t *testing.T) {
	srv, rec := recordingGateway(t, fixture.Default())

	_, err := runChatWith(context.Background(), t, srv, nil, io.Discard, "chat", "hi", "--model", "  ")
	if err == nil {
		t.Fatal("a blank --model must fail")
	}
	if n := rec.count(); n != 0 {
		t.Fatalf("a blank --model must not reach the gateway, saw %d requests", n)
	}
}

func TestChatSendsSystemAndUserMessages(t *testing.T) {
	srv, rec := recordingGateway(t, fixture.Default())

	if _, err := runChatWith(context.Background(), t, srv, nil, io.Discard,
		"chat", "how", "are", "you", "--model", "claude-sonnet-4-6", "--system", "be terse"); err != nil {
		t.Fatal(err)
	}
	var sent struct {
		Model    string `json:"model"`
		Stream   bool   `json:"stream"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(rec.chat(), &sent); err != nil {
		t.Fatalf("chat body: %v (%s)", err, rec.chat())
	}
	if sent.Model != "claude-sonnet-4-6" || !sent.Stream {
		t.Fatalf("model/stream wrong: %+v", sent)
	}
	if len(sent.Messages) != 2 {
		t.Fatalf("want system + user, got %+v", sent.Messages)
	}
	if sent.Messages[0].Role != "system" || sent.Messages[0].Content != "be terse" {
		t.Fatalf("system message: %+v", sent.Messages[0])
	}
	if sent.Messages[1].Role != "user" || sent.Messages[1].Content != "how are you" {
		t.Fatalf("prompt must be the args joined: %+v", sent.Messages[1])
	}
}

// A half-written answer that exits 0 is indistinguishable from a complete one
// to anything consuming this command — so a mid-stream failure is exit 1 with
// the reason on stderr.
func TestChatMidStreamErrorExitsNonZero(t *testing.T) {
	s := fixture.Default()
	s.ChatFails = true
	srv := gateway(t, s)

	stdout, stderr, err := run(t, srv, "chat", "hi", "--model", "claude-sonnet-4-6")
	if err == nil {
		t.Fatal("a mid-stream error frame must exit non-zero")
	}
	if !strings.Contains(stderr, "upstream error from provider anthropic") {
		t.Fatalf("stderr must carry the gateway's reason, got %q", stderr)
	}
	if !strings.Contains(stdout, "Ferro routed") {
		t.Fatalf("the part that did arrive still belongs on stdout: %q", stdout)
	}
}

// A gateway with no request-log store cannot attribute anything. That is a
// missing detail beside an answer that arrived, not a failed chat.
func TestChatWithoutLogStoreSucceedsAndOmitsProvider(t *testing.T) {
	s := fixture.Default()
	s.NoLogStore = true
	srv := gateway(t, s)

	stdout, stderr, err := run(t, srv, "chat", "hi", "--model", "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("absent attribution must not fail the chat: %v", err)
	}
	if stdout != fullReply+"\n" {
		t.Fatalf("the answer must still arrive: %q", stdout)
	}
	if !strings.Contains(stderr, "24 in / 7 out") {
		t.Fatalf("usage comes from the stream, not the log: %q", stderr)
	}
	if strings.Contains(stderr, "anthropic") || strings.Contains(stderr, "$") {
		t.Fatalf("an unattributed answer must not name a provider or a cost: %q", stderr)
	}
}

// The other two terminal codes cannot be produced by the fixture — one is the
// gateway's idle bound elapsing, the other a dropped connection — so their
// wording is asserted here. All three are errors, which is what makes them
// exit non-zero.
func TestStreamFailureWording(t *testing.T) {
	cases := []struct {
		name string
		err  api.Error
		want string
	}{
		{"timeout", api.Error{Code: api.CodeStreamTimeout}, "stream idled out — the gateway closed it after its idle bound elapsed"},
		{"incomplete", api.Error{Code: api.CodeStreamIncomplete}, "stream ended mid-answer — output is truncated"},
		{"error", api.Error{Code: api.CodeStreamError, Message: "upstream exploded"}, "stream failed: upstream exploded"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := streamFailure(&tc.err)
			if got == nil {
				t.Fatal("every terminal stream code must be an error, or a truncated answer exits 0")
			}
			if got.Error() != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

// cancelingWriter cancels as soon as the first byte of the answer lands, so
// "the operator pressed ctrl-c mid-stream" is a deterministic event rather than
// a race against a sleep.
type cancelingWriter struct {
	mu     sync.Mutex
	buf    strings.Builder
	cancel context.CancelFunc
}

func (w *cancelingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buf.Write(p)
	if w.cancel != nil {
		w.cancel()
		w.cancel = nil
	}
	return n, err
}

func (w *cancelingWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// Ctrl-C is the operator ending their own request, not a fault: exit 0, with
// the truncation stated on stderr so it is never silent. This matches
// `logs tail`, where an interrupt is likewise a clean exit.
func TestChatInterruptedIsCleanExitAndSaysSo(t *testing.T) {
	s := fixture.Default()
	s.ChatDelay = 40 * time.Millisecond
	srv := gateway(t, s)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &cancelingWriter{cancel: cancel}

	stderr, err := runChatWith(ctx, t, srv, nil, out, "chat", "hi", "--model", "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("an interrupted chat is a clean exit: %v", err)
	}
	if !strings.Contains(stderr, "interrupted") {
		t.Fatalf("an interrupted chat must say the output is truncated: %q", stderr)
	}
	if got := out.String(); got == "" || strings.Contains(got, "gateway.") {
		t.Fatalf("want the partial answer, got %q", got)
	}
}

func TestChatStructuredInterruptPrintsPartialDocumentAndFails(t *testing.T) {
	for _, format := range []string{"json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			s := fixture.Default()
			s.ChatDelay = 40 * time.Millisecond
			srv := gateway(t, s)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			timer := time.AfterFunc(150*time.Millisecond, cancel)
			defer timer.Stop()
			var out bytes.Buffer
			stderr, err := runChatWith(ctx, t, srv, nil, &out,
				"chat", "hi", "--model", "claude-sonnet-4-6", "--format", format)
			if err == nil || !strings.Contains(err.Error(), "structured output is truncated") {
				t.Fatalf("structured interruption must exit non-zero: %v", err)
			}
			if !strings.Contains(stderr, "interrupted") {
				t.Fatalf("interruption warning missing: %q", stderr)
			}
			var doc map[string]any
			if format == "json" {
				if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
					t.Fatalf("partial JSON must remain one valid document: %v (%q)", err, out.String())
				}
			} else if err := yaml.Unmarshal(out.Bytes(), &doc); err != nil {
				t.Fatalf("partial YAML must remain one valid document: %v (%q)", err, out.String())
			}
			if _, ok := doc["content"]; !ok {
				t.Fatalf("partial document lost its content field: %v", doc)
			}
		})
	}
}

func TestChatReadsPromptFromStdinWhenNotATTY(t *testing.T) {
	srv, rec := recordingGateway(t, fixture.Default())
	stdinIsTTY = func() bool { return false }
	t.Cleanup(func() { stdinIsTTY = isTerminalStdin })

	if _, err := runChatWith(context.Background(), t, srv, strings.NewReader("summarize this\n"), io.Discard,
		"chat", "--model", "claude-sonnet-4-6"); err != nil {
		t.Fatal(err)
	}
	var sent struct {
		Messages []struct{ Content string } `json:"messages"`
	}
	if err := json.Unmarshal(rec.chat(), &sent); err != nil {
		t.Fatal(err)
	}
	if len(sent.Messages) != 1 || sent.Messages[0].Content != "summarize this" {
		t.Fatalf("prompt must come from stdin: %+v", sent.Messages)
	}
}

// With no args and a terminal on stdin there is no prompt and nothing to wait
// for: say so rather than block forever on a read that will never return.
func TestChatWithNoPromptAndATTYFailsWithGuidance(t *testing.T) {
	srv, rec := recordingGateway(t, fixture.Default())
	stdinIsTTY = func() bool { return true }
	t.Cleanup(func() { stdinIsTTY = isTerminalStdin })

	_, err := runChatWith(context.Background(), t, srv, nil, io.Discard, "chat", "--model", "claude-sonnet-4-6")
	if err == nil || !strings.Contains(err.Error(), "prompt") {
		t.Fatalf("want a prompt-shaped error, got %v", err)
	}
	if n := rec.count(); n != 0 {
		t.Fatalf("a promptless chat must not reach the gateway, saw %d requests", n)
	}
}
