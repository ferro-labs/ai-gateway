package table

import (
	"fmt"
	"time"
)

// OrDash renders an empty string as "-". Every table cell that can be absent
// uses this rather than printing "": an empty cell collapses a column for
// anything splitting the table on whitespace, and reads as a blank status
// rather than as its absence.
func OrDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// CountOrDash renders a count the gateway never supplied as "-", and one it
// supplied as the number it gave — a real zero included. Taking *int rather
// than int is what keeps the two apart: the gateway never asked and the
// gateway answered zero must not print the same cell.
func CountOrDash(n *int) string {
	if n == nil {
		return "-"
	}
	return fmt.Sprintf("%d", *n)
}

// PositiveOrDash renders a measurement whose zero is not a real value — a
// model's context window, which no model publishes as 0 — so unlike
// CountOrDash a plain int is honest here: the two readings CountOrDash keeps
// apart genuinely coincide for this kind of value.
func PositiveOrDash(n int) string {
	if n <= 0 {
		return "-"
	}
	return fmt.Sprintf("%d", n)
}

// BoolYN renders a bool as the table's yes/no vocabulary.
func BoolYN(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// FmtTime renders a wire timestamp in local time. RFC3339 keeps every table
// cell whitespace-free, so a piped table still splits into fields.
func FmtTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format(time.RFC3339)
}

// FmtTimePtr renders an optional timestamp: "-" when the gateway did not
// supply one.
func FmtTimePtr(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return FmtTime(*t)
}
