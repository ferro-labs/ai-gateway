package core

import (
	"bufio"
	"io"
)

const (
	sseScannerInitialBufferSize = 64 * 1024
	sseScannerMaxTokenSize      = 1024 * 1024
)

// NewSSEScanner returns a scanner sized for large SSE data lines, such as
// tool-call payloads and long reasoning traces.
func NewSSEScanner(r io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, sseScannerInitialBufferSize), sseScannerMaxTokenSize)
	return scanner
}
