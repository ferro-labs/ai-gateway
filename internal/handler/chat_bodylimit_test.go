package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	aigateway "github.com/ferro-labs/ai-gateway"
)

// TestChatCompletions_BodyTooLarge_Returns413 verifies that when the request body is
// wrapped with http.MaxBytesReader and the payload exceeds the limit, the handler
// returns HTTP 413 (not 400 or 500).
func TestChatCompletions_BodyTooLarge_Returns413(t *testing.T) {
	gw, err := aigateway.New(aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
		Targets:  []aigateway.Target{{VirtualKey: "unused"}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = gw.Close() })

	// Use valid JSON that starts correctly but is far larger than the tiny limit.
	// The JSON decoder reads partial content then hits the MaxBytesReader limit.
	body := `{"model":"test","messages":[{"role":"user","content":"` + strings.Repeat("x", 200) + `"}]}`

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	w := httptest.NewRecorder()

	// Simulate what the body-limit middleware does: wrap the body with a 10-byte limit.
	req.Body = http.MaxBytesReader(w, req.Body, 10)

	ChatCompletions(gw)(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413, got %d (body: %s)", w.Code, w.Body.String())
	}
}

// TestDecodeChatCompletionRequest_BodyTooLarge verifies that DecodeChatCompletionRequest
// propagates *http.MaxBytesError when the body exceeds the MaxBytesReader limit,
// allowing callers to map it to 413 via errors.As.
func TestDecodeChatCompletionRequest_BodyTooLarge(t *testing.T) {
	// Use valid-JSON-prefixed body: decoder reads the first few bytes, then hits the limit.
	body := `{"model":"test","messages":[{"role":"user","content":"` + strings.Repeat("x", 500) + `"}]}`

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	w := httptest.NewRecorder()

	// Wrap with a tiny limit so the reader hits it mid-JSON.
	req.Body = http.MaxBytesReader(w, req.Body, 5)

	_, err := DecodeChatCompletionRequest(req.Body)
	if err == nil {
		t.Fatal("expected error from oversized body, got nil")
	}

	// The handler uses errors.As to detect *http.MaxBytesError.
	var maxBytesErr *http.MaxBytesError
	if !errors.As(err, &maxBytesErr) {
		t.Errorf("errors.As(*http.MaxBytesError) returned false for %T: %v", err, err)
	}
}
