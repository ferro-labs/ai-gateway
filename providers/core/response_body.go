package core

import (
	"fmt"
	"io"
)

// MaxProviderResponseBytes bounds how much of an upstream provider HTTP
// response body is read into memory, so a single oversized response cannot
// exhaust gateway memory.
const MaxProviderResponseBytes = 50 << 20 // 50 MiB

// ReadResponseBody reads up to maxBytes from r and returns an error if the
// body exceeds that limit, rather than silently truncating it. It reads one
// byte past maxBytes to detect the overflow.
func ReadResponseBody(r io.Reader, maxBytes int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("response body exceeds %d byte limit", maxBytes)
	}
	return body, nil
}
