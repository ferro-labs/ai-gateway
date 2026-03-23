package core

import (
	"bytes"
	"encoding/json"
	"io"
	"sync"
)

// bufPool holds reusable bytes.Buffer instances for JSON marshaling on the
// provider hot path. Using a pool avoids a fresh heap allocation on every
// request for the serialized request body.
var bufPool = sync.Pool{
	New: func() interface{} {
		return bytes.NewBuffer(make([]byte, 0, 2048))
	},
}

// MarshalJSON encodes v to JSON using a pooled buffer and returns the
// resulting byte slice. The caller owns the returned slice; the underlying
// buffer is returned to the pool.
func MarshalJSON(v interface{}) ([]byte, error) {
	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()

	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		buf.Reset()
		bufPool.Put(buf)
		return nil, err
	}

	// json.Encoder.Encode appends a trailing newline; trim it to match
	// json.Marshal behaviour.
	b := buf.Bytes()
	if len(b) > 0 && b[len(b)-1] == '\n' {
		b = b[:len(b)-1]
	}

	// Copy out so we can return the buffer to the pool.
	out := make([]byte, len(b))
	copy(out, b)

	buf.Reset()
	bufPool.Put(buf)
	return out, nil
}

// JSONBodyReader encodes v to JSON and returns an io.Reader over the result
// along with the content length. Call Release when done with the reader to
// return the buffer to the pool. This avoids the extra copy that MarshalJSON
// performs, making it ideal for building HTTP request bodies.
func JSONBodyReader(v interface{}) (body io.Reader, contentLen int, release func(), err error) {
	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()

	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		buf.Reset()
		bufPool.Put(buf)
		return nil, 0, nil, err
	}

	// Trim trailing newline from json.Encoder.
	b := buf.Bytes()
	if len(b) > 0 && b[len(b)-1] == '\n' {
		buf.Truncate(buf.Len() - 1)
	}

	reader := bytes.NewReader(buf.Bytes())
	n := reader.Len()
	rel := func() {
		buf.Reset()
		bufPool.Put(buf)
	}
	return reader, n, rel, nil
}
