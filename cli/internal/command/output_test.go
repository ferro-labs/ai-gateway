package command

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func newTestPrinter(format Format) (*Printer, *bytes.Buffer, *bytes.Buffer) {
	var out, errb bytes.Buffer
	return &Printer{Out: &out, Err: &errb, Format: format}, &out, &errb
}

func TestTableRendersHeaderUnderlineAndRows(t *testing.T) {
	p, out, _ := newTestPrinter(FormatTable)
	p.Table([]string{"STATE", "URL"}, [][]string{{"connected", "http://localhost:8080"}})

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("want header + underline + 1 row, got %d lines:\n%s", len(lines), out.String())
	}
	if !strings.HasPrefix(lines[0], "STATE") {
		t.Fatalf("header line: %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "-----") {
		t.Fatalf("underline must be dashes matching header width, got %q", lines[1])
	}
	if !strings.Contains(lines[2], "connected") {
		t.Fatalf("row line: %q", lines[2])
	}
}

// stdout is the machine channel: `ferro status --format json | jq` must see a
// JSON document and nothing else, no matter how much narrative was produced.
func TestJSONPurityNarrativeGoesToStderr(t *testing.T) {
	p, out, errb := newTestPrinter(FormatJSON)
	p.OK("connected in 23ms")
	p.Warn("anthropic circuit half-open")
	p.Fail("something failed")
	if err := p.JSON(map[string]any{"state": "degraded", "latency_ms": 23}); err != nil {
		t.Fatal(err)
	}

	var doc map[string]any
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("stdout must be exactly one JSON document, got %q: %v", out.String(), err)
	}
	if doc["state"] != "degraded" {
		t.Fatalf("payload lost: %v", doc)
	}
	for _, want := range []string{"connected in 23ms", "half-open", "something failed"} {
		if !strings.Contains(errb.String(), want) {
			t.Fatalf("narrative %q must reach stderr, got %q", want, errb.String())
		}
	}
}

func TestYAMLGoesToStdout(t *testing.T) {
	p, out, _ := newTestPrinter(FormatYAML)
	if err := p.YAML(map[string]string{"state": "connected"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "state: connected") {
		t.Fatalf("yaml output: %q", out.String())
	}
}

// Color must never be the only carrier of meaning, and it must be absent
// entirely when the printer is not in color mode.
func TestNoColorEmitsNoANSI(t *testing.T) {
	p, _, errb := newTestPrinter(FormatTable)
	p.Color = false
	p.OK("all good")
	p.Fail("all bad")
	if strings.Contains(errb.String(), "\x1b[") {
		t.Fatalf("ANSI leaked with Color=false: %q", errb.String())
	}
	// The glyph still distinguishes the two, so meaning survives without color.
	if !strings.Contains(errb.String(), "✓") || !strings.Contains(errb.String(), "✗") {
		t.Fatalf("glyphs must carry state when color cannot: %q", errb.String())
	}
}

func TestColorEmitsANSIWhenEnabled(t *testing.T) {
	p, _, errb := newTestPrinter(FormatTable)
	p.Color = true
	p.OK("all good")
	if !strings.Contains(errb.String(), "\x1b[") {
		t.Fatalf("want ANSI with Color=true, got %q", errb.String())
	}
}

func TestASCIIGlyphVocabulary(t *testing.T) {
	p, _, errb := newTestPrinter(FormatTable)
	p.ASCII = true
	p.OK("ok")
	p.Warn("warn")
	p.Fail("fail")
	got := errb.String()
	for _, want := range []string{"[OK]", "[!]", "[X]"} {
		if !strings.Contains(got, want) {
			t.Fatalf("ascii mode must use %s, got %q", want, got)
		}
	}
	if strings.ContainsAny(got, "✓✗") {
		t.Fatalf("ascii mode leaked unicode glyphs: %q", got)
	}
}

// The api wire types carry json tags and no yaml tags. yaml.v3 does not read
// json tags, so a naive Marshal renders total_entries as totalentries — the
// same field, named differently depending on --format.
func TestYAMLUsesJSONFieldNames(t *testing.T) {
	p, out, _ := newTestPrinter(FormatYAML)
	payload := struct {
		TotalEntries int     `json:"total_entries"`
		CostUSD      float64 `json:"cost_usd"`
		CreatedUnix  int64   `json:"created_unix"`
		TraceID      string  `json:"trace_id"`
	}{TotalEntries: 1500, CostUSD: 0.0031, CreatedUnix: 1786193769, TraceID: "tr_abc"}

	if err := p.YAML(payload); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"total_entries: 1500", "cost_usd: 0.0031", "trace_id: tr_abc"} {
		if !strings.Contains(got, want) {
			t.Fatalf("want %q in:\n%s", want, got)
		}
	}
	// Large integers must stay integers: neither quoted (json.Number leaking
	// through as a string) nor rendered in scientific notation (float64).
	if !strings.Contains(got, "created_unix: 1786193769") {
		t.Fatalf("integer precision lost:\n%s", got)
	}
	for _, bad := range []string{"totalentries", "costusd", `"1500"`, "e+09"} {
		if strings.Contains(got, bad) {
			t.Fatalf("unexpected %q in:\n%s", bad, got)
		}
	}
}

// The by_provider / by_model breakdowns reach the printer as raw gateway JSON,
// so their numbers are not bounded by any Go type this package declares. A
// number is rendered with the digits it arrived with; re-parsing it as int64 or
// float64 first would round anything that does not fit and print the result as
// though the gateway had sent it.
func TestYAMLPreservesDigitsBeyondInt64(t *testing.T) {
	p, out, _ := newTestPrinter(FormatYAML)
	payload := map[string]json.RawMessage{
		"by_provider": json.RawMessage(`{"requests":18446744073709551615,"cost_usd":0.0031}`),
	}
	if err := p.YAML(payload); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "requests: 18446744073709551615") {
		t.Fatalf("a number past int64 must survive intact, not be rounded:\n%s", got)
	}
	if !strings.Contains(got, "cost_usd: 0.0031") || strings.Contains(got, `"0.0031"`) {
		t.Fatalf("ordinary numbers must stay unquoted YAML numbers:\n%s", got)
	}
}

// NoColor takes the stream it is deciding for as a parameter: it must never
// answer for os.Stderr by inspecting os.Stdout, which is the bug that let
// `ferro status 2> run.log` leak ANSI into the redirected file while stdout
// stayed a terminal.
func TestNoColorHonoursNOCOLOREnvRegardlessOfStream(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	defer func() { _ = w.Close() }()
	// The stream must read as a terminal for this to test anything: NoColor
	// checks NO_COLOR before !isTTY, so against a bare pipe it returns true on
	// the TTY check alone and the assertion holds even if NO_COLOR were
	// ignored outright.
	orig := isTTY
	isTTY = func(*os.File) bool { return true }
	t.Cleanup(func() { isTTY = orig })
	if !NoColor(w) {
		t.Fatal("NO_COLOR must suppress colour on any stream, TTY or not")
	}
}

func TestNoColorRejectsNonTTYStream(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	defer func() { _ = w.Close() }()
	// A pipe is never a terminal, independent of NO_COLOR/TERM.
	if !NoColor(w) {
		t.Fatal("a non-TTY stream must suppress colour")
	}
}

func TestYAMLKeepsNestedNumbersNative(t *testing.T) {
	p, out, _ := newTestPrinter(FormatYAML)
	payload := map[string]any{
		"summary": map[string]any{"count": int64(9007199254740991)},
		"series":  []any{map[string]any{"value": 12.5}},
	}
	if err := p.YAML(payload); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "count: 9007199254740991") || !strings.Contains(got, "value: 12.5") ||
		strings.Contains(got, `"9007199254740991"`) {
		t.Fatalf("nested numbers lost their native YAML types:\n%s", got)
	}
}
