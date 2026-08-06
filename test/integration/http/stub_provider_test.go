//go:build integration
// +build integration

package http_test

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// Compile-time interface guards — if core.Provider changes, the build breaks.
var (
	_ core.Provider              = (*stubProvider)(nil)
	_ core.StreamProvider        = (*stubProvider)(nil)
	_ core.EmbeddingProvider     = (*stubProvider)(nil)
	_ core.ImageProvider         = (*stubProvider)(nil)
	_ core.DiscoveryProvider     = (*stubProvider)(nil)
	_ core.ProxiableProvider     = (*stubProvider)(nil)
	_ core.RerankProvider        = (*stubProvider)(nil)
	_ core.ModerationProvider    = (*stubProvider)(nil)
	_ core.TranscriptionProvider = (*stubProvider)(nil)
	_ core.SpeechProvider        = (*stubProvider)(nil)
	_ core.BatchProvider         = (*stubProvider)(nil)
)

// stubProvider is a configurable in-process provider for integration tests.
// Tests can override behavior per-call via the hook fields.
type stubProvider struct {
	mu sync.Mutex

	name         string
	models       []string
	baseURL      string
	batchBaseURL string

	// Hooks — if set, the corresponding method delegates to the hook.
	CompleteHook       func(ctx context.Context, req core.Request) (*core.Response, error)
	CompleteStreamHook func(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error)
	EmbedHook          func(ctx context.Context, req core.EmbeddingRequest) (*core.EmbeddingResponse, error)
	GenerateImageHook  func(ctx context.Context, req core.ImageRequest) (*core.ImageResponse, error)
	DiscoverModelsHook func(ctx context.Context) ([]core.ModelInfo, error)
	RerankHook         func(ctx context.Context, req core.RerankRequest) (*core.RerankResponse, error)
	ModerateHook       func(ctx context.Context, req core.ModerationRequest) (*core.ModerationResponse, error)
	TranscribeHook     func(ctx context.Context, req core.TranscriptionRequest) (*core.TranscriptionResponse, error)
	SpeechHook         func(ctx context.Context, req core.SpeechRequest) (*core.SpeechResponse, error)

	// Latency adds an artificial delay before responding (all methods).
	Latency time.Duration
}

func newStubProvider(name string, models []string) *stubProvider {
	return &stubProvider{
		name:   name,
		models: models,
	}
}

func (s *stubProvider) Name() string { return s.name }

func (s *stubProvider) ConfiguredModels() []string { return s.models }

func (s *stubProvider) SupportsModel(model string) bool {
	for _, m := range s.models {
		if m == model {
			return true
		}
	}
	return false
}

func (s *stubProvider) Models() []core.ModelInfo {
	return core.ModelsFromList(s.name, s.models)
}

func (s *stubProvider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	if s.Latency > 0 {
		time.Sleep(s.Latency)
	}
	s.mu.Lock()
	hook := s.CompleteHook
	s.mu.Unlock()
	if hook != nil {
		return hook(ctx, req)
	}
	return &core.Response{
		ID:       "stub-resp-1",
		Object:   "chat.completion",
		Model:    req.Model,
		Provider: s.name,
		Created:  time.Now().Unix(),
		Choices: []core.Choice{
			{
				Index: 0,
				Message: core.Message{
					Role:    "assistant",
					Content: "Hello from stub provider!",
				},
				FinishReason: "stop",
			},
		},
		Usage: core.Usage{
			PromptTokens:     10,
			CompletionTokens: 5,
			TotalTokens:      15,
		},
	}, nil
}

func (s *stubProvider) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
	if s.Latency > 0 {
		time.Sleep(s.Latency)
	}
	s.mu.Lock()
	hook := s.CompleteStreamHook
	s.mu.Unlock()
	if hook != nil {
		return hook(ctx, req)
	}
	ch := make(chan core.StreamChunk, 3)
	go func() {
		defer close(ch)
		for i, word := range []string{"Hello", " from", " stub!"} {
			select {
			case <-ctx.Done():
				return
			case ch <- core.StreamChunk{
				ID:      fmt.Sprintf("stub-chunk-%d", i),
				Object:  "chat.completion.chunk",
				Model:   req.Model,
				Created: time.Now().Unix(),
				Choices: []core.StreamChoice{
					{
						Index: 0,
						Delta: core.MessageDelta{Content: word},
					},
				},
			}:
			}
		}
		// Final chunk with finish_reason.
		ch <- core.StreamChunk{
			ID:      "stub-chunk-final",
			Object:  "chat.completion.chunk",
			Model:   req.Model,
			Created: time.Now().Unix(),
			Choices: []core.StreamChoice{
				{
					Index:        0,
					Delta:        core.MessageDelta{},
					FinishReason: "stop",
				},
			},
			Usage: &core.Usage{
				PromptTokens:     10,
				CompletionTokens: 3,
				TotalTokens:      13,
			},
		}
	}()
	return ch, nil
}

func (s *stubProvider) Embed(ctx context.Context, req core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	if s.Latency > 0 {
		time.Sleep(s.Latency)
	}
	s.mu.Lock()
	hook := s.EmbedHook
	s.mu.Unlock()
	if hook != nil {
		return hook(ctx, req)
	}
	return &core.EmbeddingResponse{
		Object: "list",
		Model:  req.Model,
		Data: []core.Embedding{
			{
				Object:    "embedding",
				Embedding: []float64{0.1, 0.2, 0.3, 0.4, 0.5},
				Index:     0,
			},
		},
		Usage: core.EmbeddingUsage{
			PromptTokens: 5,
			TotalTokens:  5,
		},
	}, nil
}

func (s *stubProvider) GenerateImage(ctx context.Context, req core.ImageRequest) (*core.ImageResponse, error) {
	if s.Latency > 0 {
		time.Sleep(s.Latency)
	}
	s.mu.Lock()
	hook := s.GenerateImageHook
	s.mu.Unlock()
	if hook != nil {
		return hook(ctx, req)
	}
	return &core.ImageResponse{
		Created: time.Now().Unix(),
		Data: []core.GeneratedImage{
			{B64JSON: "c3R1Yi1pbWFnZQ==", RevisedPrompt: req.Prompt},
		},
	}, nil
}

func (s *stubProvider) Rerank(ctx context.Context, req core.RerankRequest) (*core.RerankResponse, error) {
	if s.Latency > 0 {
		time.Sleep(s.Latency)
	}
	s.mu.Lock()
	hook := s.RerankHook
	s.mu.Unlock()
	if hook != nil {
		return hook(ctx, req)
	}
	results := make([]core.RerankResult, len(req.Documents))
	for i := range req.Documents {
		// Deterministic descending scores, highest for the first document.
		results[i] = core.RerankResult{Index: i, RelevanceScore: 1.0 - float64(i)*0.1}
	}
	return &core.RerankResponse{
		ID:      "stub-rerank",
		Model:   req.Model,
		Results: results,
		Meta:    &core.RerankMeta{BilledUnits: &core.RerankBilledUnits{SearchUnits: 1}},
	}, nil
}

func (s *stubProvider) Moderate(ctx context.Context, req core.ModerationRequest) (*core.ModerationResponse, error) {
	if s.Latency > 0 {
		time.Sleep(s.Latency)
	}
	s.mu.Lock()
	hook := s.ModerateHook
	s.mu.Unlock()
	if hook != nil {
		return hook(ctx, req)
	}
	return &core.ModerationResponse{
		ID:    "stub-moderation",
		Model: req.Model,
		Results: []core.ModerationResult{
			{Flagged: false, Categories: map[string]bool{"hate": false}, CategoryScores: map[string]float64{"hate": 0.001}},
		},
	}, nil
}

func (s *stubProvider) Transcribe(ctx context.Context, req core.TranscriptionRequest) (*core.TranscriptionResponse, error) {
	if s.Latency > 0 {
		time.Sleep(s.Latency)
	}
	s.mu.Lock()
	hook := s.TranscribeHook
	s.mu.Unlock()
	if hook != nil {
		return hook(ctx, req)
	}
	return &core.TranscriptionResponse{Text: "stub transcript of " + req.Filename}, nil
}

func (s *stubProvider) Speech(ctx context.Context, req core.SpeechRequest) (*core.SpeechResponse, error) {
	if s.Latency > 0 {
		time.Sleep(s.Latency)
	}
	s.mu.Lock()
	hook := s.SpeechHook
	s.mu.Unlock()
	if hook != nil {
		return hook(ctx, req)
	}
	return &core.SpeechResponse{Audio: []byte("stub-audio:" + req.Input), ContentType: "audio/mpeg"}, nil
}

func (s *stubProvider) DiscoverModels(ctx context.Context) ([]core.ModelInfo, error) {
	s.mu.Lock()
	hook := s.DiscoverModelsHook
	s.mu.Unlock()
	if hook != nil {
		return hook(ctx)
	}
	return s.Models(), nil
}

// ProxiableProvider implementation — used by proxy tests.
func (s *stubProvider) BaseURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.baseURL != "" {
		return s.baseURL
	}
	return "http://localhost:19999"
}

// SetBaseURL takes an API ROOT, not a host — the same thing core.ProxiableProvider
// asks BaseURL for. The stub stands in for an OpenAI-compatible provider, whose
// resolved root carries /v1, so callers pointing it at a test server append that
// segment as core.ResolveAPIRoot would have.
func (s *stubProvider) SetBaseURL(u string) {
	s.mu.Lock()
	s.baseURL = u
	s.mu.Unlock()
}

func (s *stubProvider) AuthHeaders() map[string]string {
	return map[string]string{"Authorization": "Bearer stub-key"}
}

// BatchProvider implementation — used by the batch/files pass-through tests. The
// base URL is set to a test upstream so a forward can be exercised end to end.
func (s *stubProvider) BatchBaseURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.batchBaseURL
}

func (s *stubProvider) SetBatchBaseURL(u string) {
	s.mu.Lock()
	s.batchBaseURL = u
	s.mu.Unlock()
}

func (s *stubProvider) BatchAuthHeaders() map[string]string {
	return map[string]string{"Authorization": "Bearer stub-batch-key"}
}
