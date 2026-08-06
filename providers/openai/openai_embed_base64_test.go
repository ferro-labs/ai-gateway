package openai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// base64EmbeddingBody builds OpenAI's REAL encoding_format=base64 response: the
// "embedding" member is a JSON STRING carrying base64 of little-endian float32s,
// not an array of numbers.
//
// The distinction is the whole point of this file. openai-go decodes into
// Embedding []float64, and its forward-compatible decoder marks a field whose
// JSON type it cannot read INVALID rather than erroring — so this body used to
// come back from Embed as err == nil with a zero-length vector, which the
// gateway served as HTTP 200 with "embedding": [] and costed at zero. Asserting
// against a float-array stub (as the previous test did) could never see that.
func base64EmbeddingBody(t *testing.T, vec []float32) string {
	t.Helper()
	var raw bytes.Buffer
	for _, f := range vec {
		if err := binary.Write(&raw, binary.LittleEndian, f); err != nil {
			t.Fatalf("binary.Write: %v", err)
		}
	}
	return fmt.Sprintf(
		`{"object":"list","model":"text-embedding-3-small","usage":{"prompt_tokens":1,"total_tokens":1},`+
			`"data":[{"object":"embedding","index":0,"embedding":%q}]}`,
		base64.StdEncoding.EncodeToString(raw.Bytes()),
	)
}

// TestOpenAIProvider_Embed_RejectsBase64 verifies encoding_format=base64 is
// refused before any upstream call, and refused as a 400-mapping typed error
// rather than a bare one that would classify as a 500.
func TestOpenAIProvider_Embed_RejectsBase64(t *testing.T) {
	var upstreamCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(base64EmbeddingBody(t, []float32{0.1, 0.2, 0.3})))
	}))
	defer srv.Close()

	p, err := New("sk-test", srv.URL)
	if err != nil {
		t.Fatalf("New(): %v", err)
	}

	resp, err := p.Embed(context.Background(), core.EmbeddingRequest{
		Model:          "text-embedding-3-small",
		Input:          "hello",
		EncodingFormat: "base64",
	})
	if err == nil {
		// This is the pre-fix behaviour: no error, and a response whose vectors
		// are empty because the SDK could not read the string into []float64.
		vecLen := -1
		if resp != nil && len(resp.Data) > 0 {
			vecLen = len(resp.Data[0].Embedding)
		}
		t.Fatalf("Embed(base64) returned no error; first embedding has %d values "+
			"(a silently empty vector served as HTTP 200 costed at zero)", vecLen)
	}
	if upstreamCalls != 0 {
		t.Errorf("upstream was called %d times; the request must be refused before it is forwarded", upstreamCalls)
	}

	// 400, not the 500 a bare error classifies as — and a status also stops
	// strategies.shouldRetry treating it as a transport failure worth retrying.
	if got := core.ParseStatusCode(err); got != http.StatusBadRequest {
		t.Errorf("ParseStatusCode(%v) = %d, want %d", err, got, http.StatusBadRequest)
	}
	var statusErr *core.HTTPStatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("Embed() error = %v (%T), want *core.HTTPStatusError", err, err)
	}
	if !strings.Contains(statusErr.Message, "base64") {
		t.Errorf("caller-facing message = %q, want it to name the rejected value", statusErr.Message)
	}
}

// TestOpenAIProvider_Embed_ForwardsFloatEncodingFormat pins that the surviving
// accepted values still reach the upstream as encoding_format=float, so the
// refusal above did not also stop the provider asking for a readable shape.
func TestOpenAIProvider_Embed_ForwardsFloatEncodingFormat(t *testing.T) {
	for _, requested := range []string{"", "float"} {
		t.Run("format="+requested, func(t *testing.T) {
			var sent string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := core.ReadResponseBody(r.Body, core.MaxProviderResponseBytes)
				if err != nil {
					t.Errorf("read request body: %v", err)
				}
				sent = string(body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"object":"list","model":"m","usage":{"prompt_tokens":1,"total_tokens":1},` +
					`"data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}]}`))
			}))
			defer srv.Close()

			p, err := New("sk-test", srv.URL)
			if err != nil {
				t.Fatalf("New(): %v", err)
			}
			resp, err := p.Embed(context.Background(), core.EmbeddingRequest{
				Model:          "text-embedding-3-small",
				Input:          "hello",
				EncodingFormat: requested,
			})
			if err != nil {
				t.Fatalf("Embed(): %v", err)
			}
			if len(resp.Data) != 1 || len(resp.Data[0].Embedding) != 2 {
				t.Fatalf("Embed() returned %+v, want one 2-value vector", resp.Data)
			}
			if !strings.Contains(sent, `"encoding_format":"float"`) {
				t.Errorf("request body = %s, want encoding_format=float", sent)
			}
		})
	}
}
