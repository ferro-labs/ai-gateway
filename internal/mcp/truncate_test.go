package mcp

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// A tool's output is arbitrary text. Truncating on a byte offset can land in the
// middle of a multi-byte rune, which would put invalid UTF-8 into a span
// attribute and an audit record.
func TestTruncateForSignalKeepsValidUTF8(t *testing.T) {
	// "…" is three bytes, so repeating it guarantees the byte cut lands
	// mid-rune for at least one of the offsets exercised below.
	for _, pad := range []int{0, 1, 2} {
		body := strings.Repeat("a", pad) + strings.Repeat("…", maxSignalLen)
		got := truncateForSignal(body)

		if !utf8.ValidString(got) {
			t.Fatalf("pad=%d: truncated text is not valid UTF-8: %q", pad, got[max(0, len(got)-16):])
		}
		if !strings.HasSuffix(got, "… (truncated)") {
			t.Fatalf("pad=%d: expected the truncation marker, got tail %q", pad, got[max(0, len(got)-16):])
		}
		if len(got) > maxSignalLen+len("… (truncated)") {
			t.Fatalf("pad=%d: result exceeds the bound: %d bytes", pad, len(got))
		}
	}
}

func TestTruncateForSignalLeavesShortTextIntact(t *testing.T) {
	const short = "tool failed: permission denied"
	if got := truncateForSignal(short); got != short {
		t.Fatalf("short text was altered: got %q", got)
	}
}
