package core

import (
	"fmt"
	"io"
)

// MaxProviderResponseBytes bounds how much of an upstream provider HTTP
// response body is read into memory, so a single oversized response cannot
// exhaust gateway memory. Applies uniformly to chat, embedding, and image
// responses; a legitimate response larger than this (e.g. a very large
// batch-embedding call) is rejected rather than allowed through uncapped.
const MaxProviderResponseBytes = 50 << 20 // 50 MiB

// ReadResponseBody reads up to maxBytes from r and returns an explicit
// "exceeds N byte limit" error if the body exceeds that limit, rather than
// silently truncating it. It reads one byte past maxBytes to detect the
// overflow.
//
// A few providers (anthropic, openai) instead wrap httpResp.Body directly in
// io.LimitReader(body, MaxProviderResponseBytes) around a streaming
// json.Decoder, to avoid this function's extra full-body copy. That path
// still bounds memory the same way, but an oversized body there surfaces as
// a generic JSON truncation/decode error rather than this function's
// explicit byte-limit message — expect a less clear error on those two
// success paths specifically.
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
