package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"gopkg.in/yaml.v3"
)

// Output format constants.
const (
	FormatTable = "table"
	FormatJSON  = "json"
	FormatYAML  = "yaml"
)

// Boolean cell labels for table output.
const (
	boolYes = "yes"
	boolNo  = "no"
)

// TableData is implemented by types that can render as an ASCII table.
type TableData interface {
	Headers() []string
	Rows() [][]string
}

// Printer formats output as table, JSON, or YAML.
type Printer struct {
	Format string
	Out    io.Writer
}

// NewPrinter creates a Printer for the given format string.
func NewPrinter(format string) *Printer {
	switch strings.ToLower(format) {
	case FormatJSON, FormatYAML:
	default:
		format = FormatTable
	}
	return &Printer{Format: format, Out: os.Stdout}
}

// Print dispatches to the appropriate encoder.
func (p *Printer) Print(v any) error {
	switch p.Format {
	case FormatJSON:
		enc := json.NewEncoder(p.Out)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	case FormatYAML:
		return yaml.NewEncoder(p.Out).Encode(v)
	default:
		if td, ok := v.(TableData); ok {
			return p.PrintTable(td)
		}
		// Fallback: pretty-print as JSON for non-TableData values.
		enc := json.NewEncoder(p.Out)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
}

// PrintTable writes aligned columns via tabwriter.
func (p *Printer) PrintTable(td TableData) error {
	w := tabwriter.NewWriter(p.Out, 0, 0, 3, ' ', 0)
	headers := td.Headers()
	_, _ = fmt.Fprintln(w, strings.Join(headers, "\t"))
	dashes := make([]string, len(headers))
	for i, h := range headers {
		dashes[i] = strings.Repeat("-", len(h))
	}
	_, _ = fmt.Fprintln(w, strings.Join(dashes, "\t"))
	for _, row := range td.Rows() {
		_, _ = fmt.Fprintln(w, strings.Join(row, "\t"))
	}
	return w.Flush()
}

// PrintSuccess writes a success indicator with a message to w.
func PrintSuccess(w io.Writer, msg string) {
	_, _ = fmt.Fprintln(w, Clr(ColorGreen, "  "+SymOK+" ")+msg)
}
