package core

import (
	"strings"
	"testing"
)

func TestNewSSEScannerAllowsLargeDataLine(t *testing.T) {
	payload := "data: " + strings.Repeat("x", 70*1024)
	scanner := NewSSEScanner(strings.NewReader(payload + "\n\n"))

	if !scanner.Scan() {
		t.Fatalf("expected scanner to read oversized SSE line, err=%v", scanner.Err())
	}
	if got := scanner.Text(); got != payload {
		t.Fatalf("scanner read %d bytes, want %d", len(got), len(payload))
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scanner error: %v", err)
	}
}
