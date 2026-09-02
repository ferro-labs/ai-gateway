package handler

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/providers"
)

// everySurfaceProvider serves every routed surface for one model, so each
// handler can be asked for the same attribution headers.
type everySurfaceProvider struct{ name string }

func (p *everySurfaceProvider) Name() string                { return p.name }
func (p *everySurfaceProvider) SupportsModel(m string) bool { return m == attributedModel }
func (p *everySurfaceProvider) Complete(_ context.Context, req providers.Request) (*providers.Response, error) {
	return &providers.Response{Model: req.Model, Choices: []providers.Choice{{Message: providers.Message{Role: "assistant", Content: "ok"}}}}, nil
}
func (p *everySurfaceProvider) CompleteStream(context.Context, providers.Request) (<-chan providers.StreamChunk, error) {
	ch := make(chan providers.StreamChunk, 1)
	ch <- providers.StreamChunk{ID: "c1", Choices: []providers.StreamChoice{{Delta: providers.MessageDelta{Content: "ok"}}}}
	close(ch)
	return ch, nil
}
func (p *everySurfaceProvider) Embed(_ context.Context, req providers.EmbeddingRequest) (*providers.EmbeddingResponse, error) {
	return &providers.EmbeddingResponse{Model: req.Model}, nil
}
func (p *everySurfaceProvider) GenerateImage(context.Context, providers.ImageRequest) (*providers.ImageResponse, error) {
	return &providers.ImageResponse{}, nil
}
func (p *everySurfaceProvider) Rerank(_ context.Context, req providers.RerankRequest) (*providers.RerankResponse, error) {
	return &providers.RerankResponse{Model: req.Model}, nil
}
func (p *everySurfaceProvider) Moderate(_ context.Context, req providers.ModerationRequest) (*providers.ModerationResponse, error) {
	return &providers.ModerationResponse{Model: req.Model}, nil
}
func (p *everySurfaceProvider) Transcribe(context.Context, providers.TranscriptionRequest) (*providers.TranscriptionResponse, error) {
	return &providers.TranscriptionResponse{Text: "ok"}, nil
}
func (p *everySurfaceProvider) Speech(context.Context, providers.SpeechRequest) (*providers.SpeechResponse, error) {
	return &providers.SpeechResponse{Audio: []byte("ok"), ContentType: "audio/mpeg"}, nil
}

const (
	attributedModel    = "attributed-visible"
	attributedUpstream = "attributed-upstream"
)

func jsonRequest(t *testing.T, path, body string) *http.Request {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r
}

func transcriptionRequest(t *testing.T) *http.Request {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("file", "audio.wav")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte("RIFF....WAVE"))
	_ = form.WriteField("model", attributedModel)
	_ = form.Close()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/audio/transcriptions", &body)
	r.Header.Set("Content-Type", form.FormDataContentType())
	return r
}

// TestAttributionHeaders_EverySurface is v1.5.2 gate 9: every routed surface
// names the target that served it. The target key is the config virtual_key,
// the provider its canonical vendor, the model the upstream id after
// model_map, and attempts the routing-layer attempt count.
func TestAttributionHeaders_EverySurface(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "primary", ModelMap: map[string]string{attributedModel: attributedUpstream}}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.RegisterProviderAs("primary", &everySurfaceProvider{name: "vendor"})

	chat := `{"model":"` + attributedModel + `","messages":[{"role":"user","content":"hi"}]}`
	surfaces := []struct {
		name    string
		handler http.HandlerFunc
		request func(t *testing.T) *http.Request
	}{
		{"chat", ChatCompletions(gw), func(t *testing.T) *http.Request { return jsonRequest(t, "/v1/chat/completions", chat) }},
		{"stream", ChatCompletions(gw), func(t *testing.T) *http.Request {
			return jsonRequest(t, "/v1/chat/completions", `{"model":"`+attributedModel+`","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
		}},
		{"completions", Completions(gw), func(t *testing.T) *http.Request {
			return jsonRequest(t, "/v1/completions", `{"model":"`+attributedModel+`","prompt":"hi"}`)
		}},
		{"embeddings", Embeddings(gw), func(t *testing.T) *http.Request {
			return jsonRequest(t, "/v1/embeddings", `{"model":"`+attributedModel+`","input":"hi"}`)
		}},
		{"images", Images(gw), func(t *testing.T) *http.Request {
			return jsonRequest(t, "/v1/images/generations", `{"model":"`+attributedModel+`","prompt":"cat"}`)
		}},
		{"rerank", Rerank(gw), func(t *testing.T) *http.Request {
			return jsonRequest(t, "/v1/rerank", `{"model":"`+attributedModel+`","query":"q","documents":["a"]}`)
		}},
		{"moderations", Moderations(gw), func(t *testing.T) *http.Request {
			return jsonRequest(t, "/v1/moderations", `{"model":"`+attributedModel+`","input":"hi"}`)
		}},
		{"transcriptions", Transcriptions(gw, false), transcriptionRequest},
		{"speech", Speech(gw), func(t *testing.T) *http.Request {
			return jsonRequest(t, "/v1/audio/speech", `{"model":"`+attributedModel+`","input":"hi","voice":"alloy"}`)
		}},
	}
	want := map[string]string{
		"X-Gateway-Provider": "vendor",
		"X-Gateway-Target":   "primary",
		"X-Gateway-Model":    attributedUpstream,
		"X-Gateway-Attempts": "1",
	}
	for _, tc := range surfaces {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			tc.handler(w, tc.request(t))
			if w.Code != http.StatusOK {
				t.Fatalf("status %d, body %s", w.Code, w.Body.String())
			}
			for header, value := range want {
				if got := w.Header().Get(header); got != value {
					t.Errorf("%s = %q, want %q", header, got, value)
				}
			}
		})
	}
}
