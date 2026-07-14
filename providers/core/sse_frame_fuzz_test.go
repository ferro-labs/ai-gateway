package core

import (
	"bytes"
	"testing"
)

// FuzzSSEDataLines exercises the SSE frame parser with arbitrary upstream
// bytes. Provider streams are untrusted input, so the scanner must iterate to
// completion for any byte sequence and report only a read error — never panic.
func FuzzSSEDataLines(f *testing.F) {
	f.Add([]byte("data: {\"id\":\"abc\"}\n\n"))
	f.Add([]byte("data: [DONE]\n\n"))
	f.Add([]byte(": keep-alive comment\ndata:hello\n\nevent: ping\n"))
	f.Add([]byte("data:\r\ndata: partial"))
	f.Add([]byte(""))

	f.Fuzz(func(_ *testing.T, data []byte) {
		seq, errFn := SSEDataLines(bytes.NewReader(data))
		for range seq { //nolint:revive // draining the iterator is the point
		}
		_ = errFn()
	})
}
