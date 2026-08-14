// Package table lays out headers and rows as an aligned, tab-padded table and
// renders the handful of "-"-for-missing-value cells that both cli/internal/tui
// and cli/internal/command print. It exists to break an import cycle:
// internal/command hands bare `ferro` off to internal/tui (see
// command/root.go's REPL wiring), so internal/tui can never import
// internal/command back — yet a verb read in the console and the same verb
// read through a pipe must render identically. This package imports neither
// of those two, and both call it instead of keeping their own copy.
package table

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// Write renders headers and rows as a plain, alignment-padded table to w: a
// header line, a dashed rule the width of each heading, then one line per
// row. No ANSI, no box drawing — this is the layout `ferro <verb>` pipes to
// stdout, as likely to be read by awk as by a person.
func Write(w io.Writer, headers []string, rows [][]string) {
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	_, _ = fmt.Fprintln(tw, strings.Join(headers, "\t"))

	dashes := make([]string, len(headers))
	for i, h := range headers {
		dashes[i] = strings.Repeat("-", len(h))
	}
	_, _ = fmt.Fprintln(tw, strings.Join(dashes, "\t"))

	for _, r := range rows {
		_, _ = fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	_ = tw.Flush()
}

// Rows renders headers and rows through the identical layout Write uses, but
// returns one string per output line — header, dashed rule, then one per data
// row — with trailing padding trimmed. It exists for a caller with no
// io.Writer of its own to hand tabwriter: the console transcript, which wraps
// each line as its own row and dims the header and its rule.
func Rows(headers []string, rows [][]string) []string {
	var buf bytes.Buffer
	Write(&buf, headers, rows)
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return lines
}
