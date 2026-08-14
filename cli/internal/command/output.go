package command

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"go.yaml.in/yaml/v3"
	"golang.org/x/term"

	"github.com/ferro-labs/ai-gateway/cli/internal/table"
)

// Format is the --format value. Report commands may refuse some of these
// rather than invent an unstable encoding of prose.
type Format string

// The three output formats. Table is for reading, JSON and YAML for piping.
const (
	FormatTable Format = "table"
	FormatJSON  Format = "json"
	FormatYAML  Format = "yaml"
)

// Printer enforces ferro's output discipline: stdout carries machine-readable
// payloads and nothing else, while human narrative, warnings, and errors go to
// stderr. That split is what makes `ferro status --format json | jq` safe.
type Printer struct {
	Out, Err io.Writer
	Format   Format
	Color    bool
	ASCII    bool
}

// isTTY reports whether f is attached to a terminal. It is the package's one
// answer to that question: the stdout/stderr split, the colour decision, and
// chat's piped-prompt detection all ask it here rather than each spelling out
// the same descriptor dance. A var, not a func, so a test can swap it in to
// simulate one stream being a terminal and another not being one -- real
// *os.File values do not let a test fake that any other way.
//
// The int conversion is imposed by the two standard APIs on either side of it —
// os.File.Fd returns a uintptr, golang.org/x/term.IsTerminal takes an int, and
// x/term offers no *os.File-shaped alternative. A descriptor never approaches
// the range where that conversion could lose information.
var isTTY = func(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }

// NoColor reports whether ANSI must be suppressed for writes to f. Any one of
// these is enough: the NO_COLOR convention, a dumb terminal, or f not being a
// TTY (a pipe or file must never receive escape codes).
//
// The decision is taken per stream rather than globally off stdout: a
// Printer's narrative (OK/Warn/Fail) always writes to p.Err, so with
// `ferro status 2> run.log` -- stdout still a terminal, stderr redirected to
// a file -- colour must be decided from stderr, or the escape codes land in
// run.log even though nothing asked for them there. Callers that draw on
// stdout (the full-screen console) still pass os.Stdout.
func NoColor(f *os.File) bool {
	if _, set := os.LookupEnv("NO_COLOR"); set {
		return true
	}
	if os.Getenv("TERM") == "dumb" {
		return true
	}
	return !isTTY(f)
}

const (
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiRed    = "\x1b[31m"
	ansiReset  = "\x1b[0m"
)

func (p *Printer) glyph(unicode, ascii string) string {
	if p.ASCII {
		return ascii
	}
	return unicode
}

// note writes one narrative line to stderr. The glyph carries the state on its
// own so meaning survives NO_COLOR, a pipe, and colour-blind readers; colour
// is only ever an enhancement.
func (p *Printer) note(color, glyph, format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	if p.Color {
		_, _ = fmt.Fprintf(p.Err, "%s%s%s %s\n", color, glyph, ansiReset, msg)
		return
	}
	_, _ = fmt.Fprintf(p.Err, "%s %s\n", glyph, msg)
}

// OK reports success on stderr, so it never contaminates a piped payload.
func (p *Printer) OK(format string, a ...any) {
	p.note(ansiGreen, p.glyph("✓", "[OK]"), format, a...)
}

// Warn reports a non-fatal condition on stderr.
func (p *Printer) Warn(format string, a ...any) {
	p.note(ansiYellow, p.glyph("!", "[!]"), format, a...)
}

// Fail reports a failure on stderr. The caller still decides the exit code.
func (p *Printer) Fail(format string, a ...any) {
	p.note(ansiRed, p.glyph("✗", "[X]"), format, a...)
}

// The shared column-header vocabulary. A fact keeps one word wherever it is
// printed — TIME is always when it happened, NAME the object's own name, STATE
// the condition ferro derived, STATUS the one the gateway reported — so a
// reader who has learnt one table can read the next, and a script locating a
// column by name finds it under that name everywhere.
const (
	colTime     = "TIME"
	colName     = "NAME"
	colState    = "STATE"
	colStatus   = "STATUS"
	colScopes   = "SCOPES"
	colExpires  = "EXPIRES"
	colProvider = "PROVIDER"
	colModels   = "MODELS"
	colURL      = "URL"
)

// Table writes a plain, alignment-padded table to stdout. No ANSI, no box
// drawing: this output is as likely to be piped into awk as read by a human.
// The layout itself lives in internal/table, shared with the console's own
// rendering of the same verb — see internal/tui/home.go's tableRows.
func (p *Printer) Table(headers []string, rows [][]string) {
	table.Write(p.Out, headers, rows)
}

// JSON writes v to stdout as one indented document.
func (p *Printer) JSON(v any) error {
	enc := json.NewEncoder(p.Out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// YAML renders v using the same key names the JSON output uses.
//
// Every wire type in internal/api carries `json:` tags and no `yaml:` tags,
// and yaml.v3 does not read json tags — it lowercases the Go field name
// instead, so total_entries would print as totalentries and cost_usd as
// costusd. Round-tripping through JSON makes one set of tags serve both
// formats, which also means a new field cannot be correct in JSON and wrong
// in YAML.
func (p *Printer) YAML(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	// UseNumber keeps integers exact; without it every number decodes as
	// float64 and a trace timestamp renders as 1.786193769e+09.
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return err
	}
	out, err := yaml.Marshal(nativeNumbers(doc))
	if err != nil {
		return err
	}
	_, err = p.Out.Write(out)
	return err
}

// nativeNumbers hands the json.Number values UseNumber preserved to yaml as
// plain scalars. Left as json.Number they are a string type and YAML would
// quote them: total_entries: "1500".
func nativeNumbers(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			t[k] = nativeNumbers(val)
		}
		return t
	case []any:
		for i, val := range t {
			t[i] = nativeNumbers(val)
		}
		return t
	case json.Number:
		// The gateway's own digits, unquoted, with nothing re-parsed in
		// between. Converting to int64 or float64 first would round anything
		// past 2^53 that is not an int64 — and the by_provider / by_model
		// breakdowns reach here as raw gateway JSON, so what fits is the
		// gateway's to decide rather than this package's to assume.
		return &yaml.Node{Kind: yaml.ScalarNode, Value: t.String()}
	default:
		return v
	}
}
