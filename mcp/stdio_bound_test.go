package mcp

import (
	"bufio"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

// boundLimit is small enough to keep the fixtures readable. What matters is
// that the bufio.Reader below is left at its default size, which is far LARGER
// than this limit: that is the arrangement in which an oversized message can
// arrive whole inside one read, and the arrangement a guard that merely reports
// an error alongside the bytes fails to catch.
const boundLimit = 64

// readLines frames the reader the way the stdio transport does, and reports
// every complete message it was willing to deliver.
func readLines(t *testing.T, input string, limit int) ([]string, error) {
	t.Helper()
	return framedLines(bufio.NewReader(&boundedLineReader{
		r: strings.NewReader(input), server: "srv", limit: limit,
	}))
}

func framedLines(r *bufio.Reader) ([]string, error) {
	var lines []string
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return lines, nil
			}
			return lines, err
		}
		lines = append(lines, line)
	}
}

// TestBoundedLineReaderPerMessage covers the guard that keeps a stdio MCP server
// from driving gateway memory: the bound is per newline-delimited message, so a
// long session of ordinary messages passes while a single oversized one does
// not. A message over the limit must never be DELIVERED — reporting an error
// after handing it over leaves the cap true of memory and false of the message.
func TestBoundedLineReaderPerMessage(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		lines   int
		wantErr bool
	}{
		{
			name:  "messages under the limit pass",
			input: strings.Repeat(strings.Repeat("a", boundLimit-1)+"\n", 20),
			lines: 20,
		},
		{
			name:  "a message of exactly the limit passes",
			input: strings.Repeat(strings.Repeat("b", boundLimit)+"\n", 5),
			lines: 5,
		},
		{
			name:    "one byte over the limit is refused, not delivered",
			input:   strings.Repeat("c", boundLimit+1) + "\n",
			wantErr: true,
		},
		{
			name:    "a message that never ends fails rather than accumulating",
			input:   strings.Repeat("d", boundLimit*100),
			wantErr: true,
		},
		{
			name:    "an oversized message after good ones still fails",
			input:   "ok\nok\n" + strings.Repeat("e", boundLimit+1) + "\n",
			lines:   2,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines, err := readLines(t, tt.input, boundLimit)

			if len(lines) != tt.lines {
				t.Errorf("delivered %d messages, want %d", len(lines), tt.lines)
			}
			for _, line := range lines {
				if got := len(strings.TrimSuffix(line, "\n")); got > boundLimit {
					t.Errorf("delivered a %d byte message, over the %d byte limit", got, boundLimit)
				}
			}
			if (err != nil) != tt.wantErr {
				t.Fatalf("terminal error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr && !strings.Contains(err.Error(), "over the") {
				t.Fatalf("error does not name the limit: %v", err)
			}
		})
	}
}

// TestBoundedLineReaderReportsTheLimitOverEOF covers a reader that delivers its
// final bytes with io.EOF attached, which io.Reader explicitly permits. The
// limit violation must survive: a plain end-of-stream sends whoever reads the
// log looking for a crashed server rather than an oversized message.
func TestBoundedLineReaderReportsTheLimitOverEOF(t *testing.T) {
	oversized := strings.Repeat("x", boundLimit*4)
	b := &boundedLineReader{
		r:      iotest.DataErrReader(strings.NewReader(oversized)),
		server: "srv",
		limit:  boundLimit,
	}

	_, err := io.ReadAll(b)
	if err == nil {
		t.Fatal("expected an error past the limit")
	}
	if errors.Is(err, io.EOF) || !strings.Contains(err.Error(), "over the") {
		t.Fatalf("limit violation was masked: %v", err)
	}
}

// TestBoundedLineReaderStaysFailed verifies the bound is terminal. Resuming
// would hand the transport the tail of a message it never saw the head of,
// leaving the framing permanently out of step.
func TestBoundedLineReaderStaysFailed(t *testing.T) {
	b := &boundedLineReader{
		r:      strings.NewReader(strings.Repeat("x", boundLimit*4) + "\nrecovered\n"),
		server: "srv",
		limit:  boundLimit,
	}

	buf := make([]byte, 256)
	if _, err := b.Read(buf); err == nil {
		t.Fatal("expected an error past the limit")
	}
	if _, err := b.Read(buf); err == nil {
		t.Fatal("reader resumed after the bound was exceeded")
	}
}

// TestBoundedLineReaderNamesTheServer verifies an operator can tell which server
// tripped the limit — the error is the only record of it.
func TestBoundedLineReaderNamesTheServer(t *testing.T) {
	b := &boundedLineReader{
		r:      strings.NewReader(strings.Repeat("x", 100)),
		server: "filesystem",
		limit:  8,
	}

	if _, err := io.ReadAll(b); err == nil || !strings.Contains(err.Error(), `"filesystem"`) {
		t.Fatalf("error does not name the server: %v", err)
	}
}
