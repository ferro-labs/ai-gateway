package streamwrap

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/circuitbreaker"
	"github.com/ferro-labs/ai-gateway/internal/metrics"
	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// feed creates a pre-filled channel of chunks and closes it.
func feed(chunks ...providers.StreamChunk) <-chan providers.StreamChunk {
	ch := make(chan providers.StreamChunk, len(chunks))
	for _, c := range chunks {
		ch <- c
	}
	close(ch)
	return ch
}

func counterValue(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	m := &dto.Metric{}
	if err := c.Write(m); err != nil {
		t.Fatalf("failed to read counter value: %v", err)
	}
	return m.GetCounter().GetValue()
}

func TestMeter_ForwardsAllChunks(t *testing.T) {
	chunks := []providers.StreamChunk{
		{ID: "1", Choices: []providers.StreamChoice{{Delta: providers.MessageDelta{Content: "hello"}}}},
		{ID: "2", Choices: []providers.StreamChoice{{Delta: providers.MessageDelta{Content: " world"}}}},
		{ID: "3", Usage: &providers.Usage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7}},
	}
	src := feed(chunks...)
	out := Meter(context.Background(), src, time.Now(), MeterMeta{
		Provider: "openai",
		Model:    "gpt-4o",
		Catalog:  models.Catalog{},
	})

	var got []providers.StreamChunk
	for c := range out {
		got = append(got, c)
	}

	if len(got) != len(chunks) {
		t.Errorf("forwarded %d chunks, want %d", len(got), len(chunks))
	}
}

func TestMeter_CallsPublishFn_OnSuccess(t *testing.T) {
	var mu sync.Mutex
	var published []map[string]interface{}

	publishFn := func(_ context.Context, _ string, data map[string]interface{}) {
		mu.Lock()
		published = append(published, data)
		mu.Unlock()
	}

	src := feed(
		providers.StreamChunk{ID: "1"},
		providers.StreamChunk{
			ID:    "2",
			Usage: &providers.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
		},
	)

	out := Meter(context.Background(), src, time.Now(), MeterMeta{
		Provider:  "openai",
		Model:     "gpt-4o",
		Catalog:   models.Catalog{},
		PublishFn: publishFn,
		TraceID:   "trace-123",
	})

	// Drain. PublishFn is called synchronously inside the Meter goroutine
	// before close(out), so once the range completes the callback has already run.
	for range out { //nolint:revive
	}

	mu.Lock()
	defer mu.Unlock()

	if len(published) != 1 {
		t.Fatalf("expected 1 publish call, got %d", len(published))
	}
	data := published[0]
	if data["provider"] != "openai" {
		t.Errorf("provider = %v, want openai", data["provider"])
	}
	if data["tokens_in"].(int) != 10 {
		t.Errorf("tokens_in = %v, want 10", data["tokens_in"])
	}
	if data["tokens_out"].(int) != 5 {
		t.Errorf("tokens_out = %v, want 5", data["tokens_out"])
	}
	if data["stream"] != true {
		t.Errorf("stream flag expected to be true")
	}
}

func TestMeter_CallsPublishFn_OnError(t *testing.T) {
	var mu sync.Mutex
	var subjects []string

	publishFn := func(_ context.Context, subject string, _ map[string]interface{}) {
		mu.Lock()
		subjects = append(subjects, subject)
		mu.Unlock()
	}

	src := feed(
		providers.StreamChunk{ID: "1"},
		providers.StreamChunk{Error: &streamError{"provider blew up"}},
	)

	out := Meter(context.Background(), src, time.Now(), MeterMeta{
		Provider:  "groq",
		Model:     "llama-3",
		Catalog:   models.Catalog{},
		PublishFn: publishFn,
	})
	// PublishFn is called synchronously before close(out); no sleep needed.
	for range out { //nolint:revive
	}

	mu.Lock()
	defer mu.Unlock()

	if len(subjects) != 1 {
		t.Fatalf("expected 1 publish call, got %d", len(subjects))
	}
	if subjects[0] != "gateway.request.failed" {
		t.Errorf("subject = %q, want gateway.request.failed", subjects[0])
	}
}

func TestMeter_IncrementsProviderErrors_OnError(t *testing.T) {
	src := feed(
		providers.StreamChunk{ID: "1"},
		providers.StreamChunk{Error: errors.New("provider failed")},
	)

	beforeReq := counterValue(t, metrics.RequestsTotal.WithLabelValues("groq", "llama-3", "error"))
	beforeProvErr := counterValue(t, metrics.ProviderErrors.WithLabelValues("groq", "provider_error"))

	out := Meter(context.Background(), src, time.Now(), MeterMeta{
		Provider: "groq",
		Model:    "llama-3",
		Catalog:  models.Catalog{},
	})
	for range out { //nolint:revive
	}

	afterReq := counterValue(t, metrics.RequestsTotal.WithLabelValues("groq", "llama-3", "error"))
	afterProvErr := counterValue(t, metrics.ProviderErrors.WithLabelValues("groq", "provider_error"))
	if afterReq-beforeReq != 1 {
		t.Fatalf("gateway_requests_total error delta = %v, want 1", afterReq-beforeReq)
	}
	if afterProvErr-beforeProvErr != 1 {
		t.Fatalf("gateway_provider_errors_total provider_error delta = %v, want 1", afterProvErr-beforeProvErr)
	}
}

func TestMeter_IncrementsProviderErrors_CircuitOpen(t *testing.T) {
	src := feed(
		providers.StreamChunk{ID: "1"},
		providers.StreamChunk{Error: circuitbreaker.ErrCircuitOpen},
	)

	beforeReq := counterValue(t, metrics.RequestsTotal.WithLabelValues("groq", "llama-3", "error"))
	beforeProvErr := counterValue(t, metrics.ProviderErrors.WithLabelValues("groq", "circuit_open"))

	out := Meter(context.Background(), src, time.Now(), MeterMeta{
		Provider: "groq",
		Model:    "llama-3",
		Catalog:  models.Catalog{},
	})
	for range out { //nolint:revive
	}

	afterReq := counterValue(t, metrics.RequestsTotal.WithLabelValues("groq", "llama-3", "error"))
	afterProvErr := counterValue(t, metrics.ProviderErrors.WithLabelValues("groq", "circuit_open"))
	if afterReq-beforeReq != 1 {
		t.Fatalf("gateway_requests_total error delta = %v, want 1", afterReq-beforeReq)
	}
	if afterProvErr-beforeProvErr != 1 {
		t.Fatalf("gateway_provider_errors_total circuit_open delta = %v, want 1", afterProvErr-beforeProvErr)
	}
}

type streamError struct{ msg string }

func (e *streamError) Error() string { return e.msg }

func TestMeter_NilPublishFn_NoPanic(t *testing.T) {
	t.Helper()
	src := feed(providers.StreamChunk{ID: "1"})
	out := Meter(context.Background(), src, time.Now(), MeterMeta{
		Provider:  "openai",
		Model:     "gpt-4o",
		Catalog:   models.Catalog{},
		PublishFn: nil,
	})
	for range out { //nolint:revive
	}
}
