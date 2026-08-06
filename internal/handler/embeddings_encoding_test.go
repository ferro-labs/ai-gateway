package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/config"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// openai-python sets encoding_format="base64" by DEFAULT — a bandwidth
// optimisation the caller never asked for and mostly cannot see. Refusing the
// value therefore made every default client.embeddings.create(...) answer 400
// against every provider, which is a worse answer than a correct vector in the
// other encoding.
//
// It is normalised rather than forwarded because Embedding.Embedding is
// []float64 and the handler JSON-encodes it directly, so a base64 vector could
// never have survived this gateway whatever the upstream returned — and
// azure-openai forwards this field verbatim.
func TestNormalizeEncodingFormat(t *testing.T) {
	for _, tc := range []struct {
		in, want string
	}{
		{"base64", "float"},
		{"float", "float"},
		{"", ""},
		// Left as written so ValidateEmbeddingEncodingFormat refuses it, which
		// keeps one rejection point rather than two.
		{"binary", "binary"},
	} {
		if got := core.NormalizeEncodingFormat(tc.in); got != tc.want {
			t.Errorf("NormalizeEncodingFormat(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	// The normalised value must pass the validator every provider applies;
	// otherwise the boundary would accept what the provider then refuses.
	if err := core.ValidateEmbeddingEncodingFormat(core.NormalizeEncodingFormat("base64")); err != nil {
		t.Errorf("a normalised base64 request is still refused downstream: %v", err)
	}
	// And a genuinely unsupported value is still refused.
	if err := core.ValidateEmbeddingEncodingFormat(core.NormalizeEncodingFormat("binary")); err == nil {
		t.Error("an unsupported encoding_format was accepted")
	}
}

// recordingEmbedder captures the encoding_format the provider actually receives.
type recordingEmbedder struct {
	seen chan string
}

func (recordingEmbedder) Name() string              { return "rec" }
func (recordingEmbedder) SupportsModel(string) bool { return true }
func (recordingEmbedder) Complete(context.Context, core.Request) (*core.Response, error) {
	return nil, errors.New("not used")
}

func (p recordingEmbedder) Embed(_ context.Context, req core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	p.seen <- req.EncodingFormat
	if err := core.ValidateEmbeddingEncodingFormat(req.EncodingFormat); err != nil {
		return nil, err
	}
	return &core.EmbeddingResponse{
		Object: "list",
		Model:  req.Model,
		Data:   []core.Embedding{{Object: "embedding", Embedding: []float64{0.5, -0.5}, Index: 0}},
	}, nil
}

// The end-to-end half: the unit test above proves the mapping, this proves it is
// wired in. Without it the normalisation could be correct and unreachable.
func TestEmbeddings_Base64IsServedAsFloat(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "rec", Models: []string{"any-model"}}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	seen := make(chan string, 1)
	gw.RegisterProvider(recordingEmbedder{seen: seen})

	body := `{"model":"any-model","input":"hello","encoding_format":"base64"}`
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/embeddings", strings.NewReader(body))
	w := httptest.NewRecorder()
	Embeddings(gw)(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	if got := <-seen; got != "float" {
		t.Errorf("provider received encoding_format=%q, want float", got)
	}

	// The caller gets a float array, which openai-python passes through
	// untouched: it base64-decodes only a value that IS a string.
	var resp core.EmbeddingResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data) != 1 || len(resp.Data[0].Embedding) != 2 {
		t.Fatalf("embedding = %v, want two floats", resp.Data)
	}
}
