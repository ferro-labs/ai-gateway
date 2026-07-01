package core

import (
	"io"
	"iter"
	"strings"
)

// SSEDataLines returns an iterator over the payloads of the "data:" lines in
// the SSE stream r, with the leading "data:" prefix removed and a single
// optional space after the colon stripped (that space is optional per the SSE
// spec); lines lacking the prefix are skipped. The returned function reports
// the scanner's read error, if any, and should be consulted once the iteration
// has finished.
func SSEDataLines(r io.Reader) (iter.Seq[string], func() error) {
	scanner := NewSSEScanner(r)
	var scanErr error
	seq := func(yield func(string) bool) {
		for scanner.Scan() {
			data, ok := strings.CutPrefix(scanner.Text(), "data:")
			if !ok {
				continue
			}
			// The SSE spec strips a single optional space after the colon.
			data = strings.TrimPrefix(data, " ")
			if !yield(data) {
				return
			}
		}
		scanErr = scanner.Err()
	}
	return seq, func() error { return scanErr }
}
