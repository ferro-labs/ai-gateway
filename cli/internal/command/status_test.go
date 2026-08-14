package command

import (
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/cli/internal/fixture"
)

// gateway starts the one in-repo definition of the wire contract. Every command
// test talks to it instead of a hand-rolled stub, so a fixture that drifts from
// the gateway fails these tests rather than quietly blessing the drift.
func gateway(t *testing.T, s fixture.State) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(fixture.Handler(s))
	t.Cleanup(srv.Close)
	return srv
}

// run executes a verb against srv with the fixture's credential.
func run(t *testing.T, srv *httptest.Server, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	t.Setenv("FERRO_API_KEY", "fgw_test")
	return execute(t, append(args, "--gateway-url", srv.URL)...)
}

// decodeOne decodes stdout into v and fails if a second document follows: the
// stdout-is-the-machine-channel rule means exactly one payload, never a payload
// with narrative wrapped around it.
func decodeOne(stdout string, v any) error {
	dec := json.NewDecoder(strings.NewReader(stdout))
	if err := dec.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return err
		}
		return errMoreThanOneDoc
	}
	return nil
}

var errMoreThanOneDoc = errors.New("stdout carried more than one JSON document")

// oneJSONDoc asserts stdout is exactly one JSON document — the whole point of
// keeping narrative on stderr.
func oneJSONDoc(t *testing.T, stdout string) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := decodeOne(stdout, &doc); err != nil {
		t.Fatalf("stdout must be one JSON document, got %q: %v", stdout, err)
	}
	return doc
}

func TestStatusTableExitAndColumns(t *testing.T) {
	srv := gateway(t, fixture.Default())
	stdout, _, err := run(t, srv, "status")
	if err != nil {
		t.Fatalf("healthy status must exit 0: %v", err)
	}
	for _, col := range []string{"STATE", "URL", "LATENCY", "TARGETS", "PROVIDERS", "MODELS", "MCP", "AUTH"} {
		if !strings.Contains(stdout, col) {
			t.Fatalf("missing column %s in %q", col, stdout)
		}
	}
	if !strings.Contains(stdout, "connected") {
		t.Fatalf("healthy gateway must report connected: %q", stdout)
	}
}

func TestStatusJSONIsPureAndStable(t *testing.T) {
	srv := gateway(t, fixture.Default())
	stdout, _, err := run(t, srv, "status", "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	doc := oneJSONDoc(t, stdout)
	for _, k := range []string{"state", "url", "latency_ms", "targets", "circuits"} {
		if _, ok := doc[k]; !ok {
			t.Fatalf("frozen status contract missing %q: %v", k, doc)
		}
	}
}

// Degraded is not a failure: `ferro status || alert` must mean "unreachable",
// so a half-open circuit still exits 0 with the degraded state on stdout.
func TestStatusDegradedExits0AndWarnsOnStderr(t *testing.T) {
	s := fixture.Default()
	s.Degraded = true
	srv := gateway(t, s)

	stdout, stderr, err := run(t, srv, "status")
	if err != nil {
		t.Fatalf("degraded must still exit 0: %v", err)
	}
	if !strings.Contains(stdout, "degraded") {
		t.Fatalf("degraded state must be printed: %q", stdout)
	}
	if !strings.Contains(stderr, "half-open") {
		t.Fatalf("warnings belong on stderr, got %q", stderr)
	}
	if strings.Contains(stdout, "half-open") {
		t.Fatalf("narrative leaked into stdout: %q", stdout)
	}
}

func TestStatusUnreachableExits1ButStillReportsState(t *testing.T) {
	stdout, _, err := execute(t, "status", "--gateway-url", "http://127.0.0.1:1", "--format", "json")
	if err == nil {
		t.Fatal("unreachable must exit 1")
	}
	doc := oneJSONDoc(t, stdout)
	if doc["state"] != "unreachable" {
		t.Fatalf("the report must still name the state that was reached: %v", doc)
	}
}

// A gateway reachable without a usable credential is still reachable: the
// report degrades to what /health and /readyz say rather than failing.
func TestStatusWithoutCredentialDegradesNotFails(t *testing.T) {
	srv := gateway(t, fixture.Default())
	t.Setenv("FERRO_API_KEY", "fgw_wrong")
	stdout, _, err := execute(t, "status", "--gateway-url", srv.URL)
	if err != nil {
		t.Fatalf("a bad credential must not make status exit 1: %v", err)
	}
	if !strings.Contains(stdout, "unauthorized") {
		t.Fatalf("auth column must report unauthorized: %q", stdout)
	}
}
