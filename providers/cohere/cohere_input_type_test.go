package cohere

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// captureEmbedInputType runs one Embed against a stub server and returns the
// input_type the provider sent.
func captureEmbedInputType(t *testing.T, reqInputType string) (string, error) {
	t.Helper()
	var captured struct {
		InputType string `json:"input_type"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"x","embeddings":[[0.1,0.2]],"texts":["hi"],"meta":{"billed_units":{"input_tokens":1}}}`)
	}))
	defer srv.Close()

	p, err := New("test-key", srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = p.Embed(context.Background(), core.EmbeddingRequest{
		Model:     "embed-english-v3.0",
		Input:     "hi",
		InputType: reqInputType,
	})
	return captured.InputType, err
}

// TestEmbed_InputTypeDefaultAndOverride verifies #145: input_type defaults to
// search_document but can be overridden (e.g. search_query for retrieval).
func TestEmbed_InputTypeDefaultAndOverride(t *testing.T) {
	t.Run("defaults to search_document", func(t *testing.T) {
		got, err := captureEmbedInputType(t, "")
		if err != nil {
			t.Fatalf("Embed: %v", err)
		}
		if got != "search_document" {
			t.Errorf("input_type = %q, want search_document", got)
		}
	})

	t.Run("honors search_query override", func(t *testing.T) {
		got, err := captureEmbedInputType(t, "search_query")
		if err != nil {
			t.Fatalf("Embed: %v", err)
		}
		if got != "search_query" {
			t.Errorf("input_type = %q, want search_query", got)
		}
	})

	t.Run("rejects unknown input_type", func(t *testing.T) {
		_, err := captureEmbedInputType(t, "bogus")
		if err == nil {
			t.Fatal("expected error for unknown input_type, got nil")
		}
	})
}
