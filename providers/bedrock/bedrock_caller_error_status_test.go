package bedrock

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// TestCallerErrorsCarry400 pins the classification of every refusal this
// provider decides itself, before any AWS call.
//
// Each of these used to be a bare fmt.Errorf. A bare error carries no status, so
// internal/apierror reported it as a 500 — telling the caller the gateway was
// broken when what they sent was a model or a parameter this provider does not
// serve — and strategies.shouldRetry treats a status-less error as a transport
// failure, so the whole retry budget was spent re-asking a question whose answer
// cannot change.
func TestCallerErrorsCarry400(t *testing.T) {
	dims := 512
	tests := []struct {
		name    string
		call    func(p *Provider) error
		wantMsg string
	}{
		{
			name: "stream on a non-anthropic model",
			call: func(p *Provider) error {
				_, err := p.CompleteStream(context.Background(), core.Request{
					Model:    "amazon.titan-text-express-v1",
					Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
				})
				return err
			},
			wantMsg: "anthropic.claude",
		},
		{
			name: "embed with a model that is not an embedding model",
			call: func(p *Provider) error {
				_, err := p.Embed(context.Background(), core.EmbeddingRequest{
					Model: "anthropic.claude-3-5-sonnet-20240620-v1:0",
					Input: "hi",
				})
				return err
			},
			wantMsg: "unsupported Bedrock embedding model",
		},
		{
			name: "embed with an encoding_format Bedrock cannot return",
			call: func(p *Provider) error {
				_, err := p.Embed(context.Background(), core.EmbeddingRequest{
					Model:          "amazon.titan-embed-text-v2:0",
					Input:          "hi",
					EncodingFormat: "base64",
				})
				return err
			},
			wantMsg: "base64",
		},
		{
			name: "titan v1 with dimensions",
			call: func(p *Provider) error {
				_, err := p.Embed(context.Background(), core.EmbeddingRequest{
					Model:      "amazon.titan-embed-text-v1",
					Input:      "hi",
					Dimensions: &dims,
				})
				return err
			},
			wantMsg: "amazon.titan-embed-text-v2",
		},
		{
			name: "cohere embeddings with dimensions",
			call: func(p *Provider) error {
				_, err := p.Embed(context.Background(), core.EmbeddingRequest{
					Model:      "cohere.embed-english-v3",
					Input:      "hi",
					Dimensions: &dims,
				})
				return err
			},
			wantMsg: "dimensions are not supported",
		},
		{
			name: "cohere embeddings with an unknown input_type",
			call: func(p *Provider) error {
				_, err := p.Embed(context.Background(), core.EmbeddingRequest{
					Model:     "cohere.embed-english-v3",
					Input:     "hi",
					InputType: "search_documents",
				})
				return err
			},
			wantMsg: "search_documents",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// No client: every case below must be refused before AWS is called,
			// so a nil client is the assertion that it is.
			p := &Provider{name: Name}

			err := tt.call(p)
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			if got := core.ParseStatusCode(err); got != http.StatusBadRequest {
				t.Errorf("ParseStatusCode(%v) = %d, want %d", err, got, http.StatusBadRequest)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("error = %q, want it to mention %q", err.Error(), tt.wantMsg)
			}
		})
	}
}
