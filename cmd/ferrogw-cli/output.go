package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"gopkg.in/yaml.v3"
)

// outputFormat controls how command results are printed.
type outputFormat string

const (
	formatTable outputFormat = "table"
	formatJSON  outputFormat = "json"
	formatYAML  outputFormat = "yaml"
)

// printer handles structured output across all CLI commands.
type printer struct {
	format outputFormat
	out    io.Writer
}

// newPrinter creates a printer for the requested format string.
// Defaults to table for unrecognised values.
func newPrinter(format string) *printer {
	f := outputFormat(strings.ToLower(format))
	switch f {
	case formatJSON, formatYAML:
	default:
		f = formatTable
	}
	return &printer{format: f, out: cliStdout}
}

// Print serialises v according to the chosen output format.
//
//   - table: v must implement TableData (Headers() and Rows())
//   - json:  indented JSON
//   - yaml:  YAML
func (p *printer) Print(v interface{}) error {
	switch p.format {
	case formatJSON:
		enc := json.NewEncoder(p.out)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	case formatYAML:
		return yaml.NewEncoder(p.out).Encode(v)
	default:
		// For table format, v is expected to implement TableData.
		if td, ok := v.(TableData); ok {
			return p.printTable(td)
		}
		// Fallback: pretty-print as JSON.
		enc := json.NewEncoder(p.out)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
}

// TableData is implemented by response types that know how to render as a table.
type TableData interface {
	Headers() []string
	Rows() [][]string
}

// printTable renders headers and rows using tabwriter for aligned columns.
func (p *printer) printTable(td TableData) error {
	w := tabwriter.NewWriter(p.out, 0, 0, 3, ' ', 0)
	_, _ = fmt.Fprintln(w, strings.Join(td.Headers(), "\t"))
	_, _ = fmt.Fprintln(w, strings.Repeat("-\t", len(td.Headers())))
	for _, row := range td.Rows() {
		_, _ = fmt.Fprintln(w, strings.Join(row, "\t"))
	}
	return w.Flush()
}

// printSuccess prints a simple success message to stdout.
func printSuccess(msg string) {
	fmt.Fprintln(cliStdout, msg)
}
