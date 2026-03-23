package transport

import (
	"bytes"
	"sync"
)

// BufferPool is a pool of reusable byte buffers for hot path use.
// Eliminates per-request heap allocations for request/response bodies.
var BufferPool = newBufferPool()

type bufferPool struct {
	p sync.Pool
}

func newBufferPool() *bufferPool {
	return &bufferPool{
		p: sync.Pool{
			New: func() interface{} {
				// 32KB is a good default for LLM request/response bodies.
				return bytes.NewBuffer(make([]byte, 0, 32*1024))
			},
		},
	}
}

// Get retrieves a reset buffer from the pool.
func (bp *bufferPool) Get() *bytes.Buffer {
	buf := bp.p.Get().(*bytes.Buffer)
	buf.Reset()
	return buf
}

// Put returns a buffer to the pool.
// Buffers over 1MB are discarded to prevent memory bloat.
func (bp *bufferPool) Put(buf *bytes.Buffer) {
	if buf.Cap() > 1024*1024 {
		return
	}
	bp.p.Put(buf)
}
