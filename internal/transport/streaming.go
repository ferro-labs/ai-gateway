package transport

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// StreamingConfig holds SSE-specific transport tuning.
type StreamingConfig struct {
	// IdleTimeout is how long to keep idle connections for streaming.
	// Longer than non-streaming because LLM responses are bursty —
	// a connection idle between chunks should not be recycled.
	IdleTimeout time.Duration

	// ReadBufferSize is the bufio.Reader size for SSE chunk reads.
	// Larger buffers reduce syscalls for high-throughput streams.
	ReadBufferSize int

	// WriteBufferSize is the bufio.Writer size for SSE writes to clients.
	WriteBufferSize int

	// MaxIdleConnsPerHost for the streaming transport.
	// Streaming connections are long-lived, so fewer idle connections suffice.
	MaxIdleConnsPerHost int
}

// DefaultStreamingConfig returns production-optimized SSE defaults.
func DefaultStreamingConfig() StreamingConfig {
	return StreamingConfig{
		IdleTimeout:         5 * time.Minute,
		ReadBufferSize:      64 * 1024, // 64KB — SSE chunks can be large for tool-call responses
		WriteBufferSize:     4 * 1024,  // 4KB — matches sse.go's bufio.NewWriterSize
		MaxIdleConnsPerHost: 50,
	}
}

// StreamTransport wraps the SSE-optimized http.Transport with additional
// configuration for reading streamed responses efficiently.
type StreamTransport struct {
	client    *http.Client
	cfg       StreamingConfig
	readerBuf sync.Pool
	writerBuf sync.Pool
}

// NewStreamTransport creates an SSE-optimized transport from the given config.
func NewStreamTransport(base Config, sse StreamingConfig) *StreamTransport {
	dialer := &net.Dialer{
		Timeout:   base.DialTimeout,
		KeepAlive: base.KeepAliveInterval,
	}

	maxIdle := sse.MaxIdleConnsPerHost
	if maxIdle == 0 {
		maxIdle = base.MaxIdleConnsPerHost
	}

	t := &http.Transport{
		DialContext:         dialer.DialContext,
		MaxIdleConns:        base.MaxIdleConns,
		MaxIdleConnsPerHost: maxIdle,
		MaxConnsPerHost:     base.MaxConnsPerHost,
		IdleConnTimeout:     sse.IdleTimeout,
		TLSHandshakeTimeout: base.TLSHandshakeTimeout,
		ForceAttemptHTTP2:   base.ForceHTTP2,
		DisableKeepAlives:   false,
		DisableCompression:  base.DisableCompression,
		// No ResponseHeaderTimeout — LLM first-token latency can be 10-30s.
		// No ExpectContinueTimeout — streaming requests are always POST with body.
	}

	readBufSize := sse.ReadBufferSize
	if readBufSize == 0 {
		readBufSize = 64 * 1024
	}
	writeBufSize := sse.WriteBufferSize
	if writeBufSize == 0 {
		writeBufSize = 4 * 1024
	}

	st := &StreamTransport{
		client: &http.Client{Transport: t},
		cfg:    sse,
		readerBuf: sync.Pool{
			New: func() interface{} {
				return bufio.NewReaderSize(nil, readBufSize)
			},
		},
		writerBuf: sync.Pool{
			New: func() interface{} {
				return bufio.NewWriterSize(nil, writeBufSize)
			},
		},
	}

	return st
}

// Client returns the SSE-optimized HTTP client.
func (st *StreamTransport) Client() *http.Client {
	return st.client
}

// GetReader returns a pooled bufio.Reader sized for SSE chunk reads.
// Call PutReader when the stream is fully consumed.
func (st *StreamTransport) GetReader(r io.Reader) *bufio.Reader {
	br := st.readerBuf.Get().(*bufio.Reader)
	br.Reset(r)
	return br
}

// PutReader returns a bufio.Reader to the pool.
func (st *StreamTransport) PutReader(br *bufio.Reader) {
	br.Reset(nil) // release reference to underlying reader
	st.readerBuf.Put(br)
}

// GetWriter returns a pooled bufio.Writer sized for SSE writes.
// Call PutWriter when the stream is fully flushed.
func (st *StreamTransport) GetWriter(w io.Writer) *bufio.Writer {
	bw := st.writerBuf.Get().(*bufio.Writer)
	bw.Reset(w)
	return bw
}

// PutWriter returns a bufio.Writer to the pool.
func (st *StreamTransport) PutWriter(bw *bufio.Writer) {
	bw.Reset(nil) // release reference to underlying writer
	st.writerBuf.Put(bw)
}

// CloseIdleConnections closes idle streaming connections.
func (st *StreamTransport) CloseIdleConnections() {
	st.client.CloseIdleConnections()
}

// IsStreamingRequest returns true if the request body contains
// "stream":true in any whitespace variation.
// Zero allocations — does not parse JSON, uses byte scanning only.
func IsStreamingRequest(body []byte) bool {
	// scan for "stream" then look for true after the colon
	for i := 0; i < len(body)-10; i++ {
		if body[i] != 's' {
			continue
		}
		if i+6 > len(body) || string(body[i:i+6]) != "stream" {
			continue
		}
		// found "stream" — scan forward for colon then true/false
		for j := i + 6; j < len(body) && j < i+30; j++ {
			switch body[j] {
			case ' ', '\t', '\n', '\r', '"', ':':
				continue
			case 't':
				return j+4 <= len(body) && string(body[j:j+4]) == "true"
			case 'f':
				return false
			}
		}
	}
	return false
}
