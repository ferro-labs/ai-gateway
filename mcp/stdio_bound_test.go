package mcp

import (
	"bufio"
	"errors"
	"io"
	"strings"
	"testing"
)

// TestBoundedLineReaderPerMessage covers the guard that keeps a stdio MCP
// server from driving gateway memory: the bound is per newline-delimited
// message, so a long session of ordinary messages must pass while a single
// oversized one must fail. Read through bufio.Reader.ReadString because that is
// exactly how the transport frames JSON-RPC.
func TestBoundedLineReaderPerMessage(t *testing.T) {
	const limit = 64

	tests := []struct {
		name    string
		input   string
		lines   int
		wantErr bool
	}{
		{
			name:  "messages under the limit pass",
			input: strings.Repeat(strings.Repeat("a", limit-1)+"\n", 20),
			lines: 20,
		},
		{
			name:  "the count resets at every newline",
			input: strings.Repeat(strings.Repeat("b", limit)+"\n", 100),
			lines: 100,
		},
		{
			name:    "one oversized message fails the read",
			input:   strings.Repeat("c", limit*4) + "\n",
			wantErr: true,
		},
		{
			name:    "a message that never ends fails rather than accumulating",
			input:   strings.Repeat("d", limit*100),
			wantErr: true,
		},
		{
			name:    "an oversized message after good ones still fails",
			input:   "ok\nok\n" + strings.Repeat("e", limit*2) + "\n",
			lines:   2,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &boundedLineReader{r: strings.NewReader(tt.input), server: "srv", limit: limit}
			// A buffer well under the limit, as the transport's is: a read can
			// then never deliver a whole oversized message before the guard
			// reports it, which is the property that keeps it from being read
			// into memory at all.
			r := bufio.NewReaderSize(b, 16)

			var (
				read int
				err  error
			)
			for err == nil {
				if _, err = r.ReadString('\n'); err == nil {
					read++
				}
			}

			if read != tt.lines {
				t.Errorf("read %d complete messages, want %d", read, tt.lines)
			}
			gotErr := err != nil && !errors.Is(err, io.EOF)
			if gotErr != tt.wantErr {
				t.Fatalf("terminal error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr && !strings.Contains(err.Error(), "over the") {
				t.Fatalf("error does not name the limit: %v", err)
			}
		})
	}
}

// TestBoundedLineReaderNamesTheServer verifies an operator can tell which server
// tripped the limit — the error is the only record of it.
func TestBoundedLineReaderNamesTheServer(t *testing.T) {
	b := &boundedLineReader{r: strings.NewReader(strings.Repeat("x", 100)), server: "filesystem", limit: 8}
	_, err := io.ReadAll(b)
	if err == nil {
		t.Fatal("expected an error past the limit")
	}
	if !strings.Contains(err.Error(), `"filesystem"`) {
		t.Fatalf("error does not name the server: %v", err)
	}
}
