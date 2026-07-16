package streamwrap

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/circuitbreaker"
	"github.com/ferro-labs/ai-gateway/internal/events"
	"github.com/ferro-labs/ai-gateway/internal/metrics"
	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/plugin"
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

// ptrF returns a *float64 from a literal — helper for building catalog fixtures.
func ptrF(v float64) *float64 { return &v }

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
		Provider:    "openai",
		Model:       "gpt-4o",
		MetricModel: "gpt-4o",
		Catalog:     models.Catalog{},
	})

	var got []providers.StreamChunk
	for c := range out {
		got = append(got, c)
	}

	if len(got) != len(chunks) {
		t.Errorf("forwarded %d chunks, want %d", len(got), len(chunks))
	}
}

// TestMeter_CostLookupPrefersCatalogProviderOverProvider verifies that when
// CatalogProvider is set it is used for the catalog/cost lookup instead of
// Provider — Provider may deliberately be a routing alias (e.g.
// "ollama-cloud-a") kept for metrics/log labels, while CatalogProvider
// carries the canonical provider type the catalog is actually keyed by.
func TestMeter_CostLookupPrefersCatalogProviderOverProvider(t *testing.T) {
	catalog := models.Catalog{
		// Keyed by canonical type only — no entry for the alias.
		"ollama-cloud/llama3": {
			Provider: "ollama-cloud",
			ModelID:  "llama3",
			Mode:     models.ModeChat,
			Pricing: models.Pricing{
				InputPerMTokens:  ptrF(1.0),
				OutputPerMTokens: ptrF(2.0),
			},
		},
	}

	src := feed(
		providers.StreamChunk{ID: "1"},
		providers.StreamChunk{
			ID:    "2",
			Usage: &providers.Usage{PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500},
		},
	)

	costCounter := metrics.ForRequest("ollama-cloud-a", "llama3").CostUSD
	before := counterValue(t, costCounter)

	out := Meter(context.Background(), src, time.Now(), MeterMeta{
		Provider:        "ollama-cloud-a", // routing alias, kept for labels
		CatalogProvider: "ollama-cloud",   // canonical type, used for cost lookup
		Model:           "llama3",
		MetricModel:     "llama3",
		Catalog:         catalog,
	})
	for range out { //nolint:revive // empty-block: intentionally draining the stream to completion
	}

	after := counterValue(t, costCounter)
	if after <= before {
		t.Fatalf("expected cost counter to increase via CatalogProvider-resolved catalog lookup, before=%v after=%v", before, after)
	}
}

// TestMeter_CostLookupFallsBackToProviderWhenCatalogProviderUnset confirms
// existing construction sites (which never set CatalogProvider) keep their
// current behavior: cost lookup falls back to Provider unchanged.
func TestMeter_CostLookupFallsBackToProviderWhenCatalogProviderUnset(t *testing.T) {
	catalog := models.Catalog{
		"openai/gpt-4o": {
			Provider: "openai",
			ModelID:  "gpt-4o",
			Mode:     models.ModeChat,
			Pricing: models.Pricing{
				InputPerMTokens:  ptrF(1.0),
				OutputPerMTokens: ptrF(2.0),
			},
		},
	}

	src := feed(
		providers.StreamChunk{ID: "1"},
		providers.StreamChunk{
			ID:    "2",
			Usage: &providers.Usage{PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500},
		},
	)

	costCounter := metrics.ForRequest("openai", "gpt-4o").CostUSD
	before := counterValue(t, costCounter)

	out := Meter(context.Background(), src, time.Now(), MeterMeta{
		Provider:    "openai", // CatalogProvider left unset
		Model:       "gpt-4o",
		MetricModel: "gpt-4o",
		Catalog:     catalog,
	})
	for range out { //nolint:revive // empty-block: intentionally draining the stream to completion
	}

	after := counterValue(t, costCounter)
	if after <= before {
		t.Fatalf("expected cost counter to increase via Provider-resolved catalog lookup, before=%v after=%v", before, after)
	}
}

func TestMeter_CallsPublishFn_OnSuccess(t *testing.T) {
	var mu sync.Mutex
	var published []map[string]any

	publishFn := func(_ context.Context, event events.HookEvent) {
		mu.Lock()
		published = append(published, event.Map())
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
		Provider:    "openai",
		Model:       "gpt-4o",
		MetricModel: "gpt-4o",
		Catalog:     models.Catalog{},
		PublishFn:   publishFn,
		TraceID:     "trace-123",
	})

	// Drain. PublishFn is called synchronously inside the Meter goroutine
	// before close(out), so once the range completes the callback has already run.
	for range out { //nolint:revive // empty-block: intentionally draining the stream to completion
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

	publishFn := func(_ context.Context, event events.HookEvent) {
		mu.Lock()
		subjects = append(subjects, event.Subject)
		mu.Unlock()
	}

	src := feed(
		providers.StreamChunk{ID: "1"},
		providers.StreamChunk{Error: &streamError{"provider blew up"}},
	)

	out := Meter(context.Background(), src, time.Now(), MeterMeta{
		Provider:    "groq",
		Model:       "llama-3",
		MetricModel: "llama-3",
		Catalog:     models.Catalog{},
		PublishFn:   publishFn,
	})
	// PublishFn is called synchronously before close(out); no sleep needed.
	for range out { //nolint:revive // empty-block: intentionally draining the stream to completion
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
		Provider:    "groq",
		Model:       "llama-3",
		MetricModel: "llama-3",
		Catalog:     models.Catalog{},
	})
	for range out { //nolint:revive // empty-block: intentionally draining the stream to completion
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
		Provider:    "groq",
		Model:       "llama-3",
		MetricModel: "llama-3",
		Catalog:     models.Catalog{},
	})
	for range out { //nolint:revive // empty-block: intentionally draining the stream to completion
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

func TestMeter_CircuitBreakerOutcome_PreservesProviderErrorOnClientCancel(t *testing.T) {
	providerErr := errors.New("provider blew up")
	src := make(chan providers.StreamChunk)

	sendErr := make(chan struct{})
	go func() {
		src <- providers.StreamChunk{ID: "1"}
		<-sendErr
		src <- providers.StreamChunk{Error: providerErr}
		close(src)
	}()

	ctx, cancel := context.WithCancel(context.Background())

	var mu sync.Mutex
	var outcomes []error
	outcomeFn := func(err error) {
		mu.Lock()
		outcomes = append(outcomes, err)
		mu.Unlock()
	}

	out := Meter(ctx, src, time.Now(), MeterMeta{
		Provider:              "openai",
		Model:                 "gpt-4o",
		MetricModel:           "gpt-4o",
		Catalog:               models.Catalog{},
		CircuitBreakerOutcome: outcomeFn,
	})

	if _, ok := <-out; !ok {
		t.Fatal("expected first chunk")
	}
	cancel()
	close(sendErr)

	for range out { //nolint:revive // empty-block: intentionally draining the stream to completion
	}

	mu.Lock()
	defer mu.Unlock()
	if len(outcomes) != 1 {
		t.Fatalf("outcomes len = %d, want 1", len(outcomes))
	}
	if !errors.Is(outcomes[0], providerErr) {
		t.Fatalf("outcome = %v, want provider error", outcomes[0])
	}
}

func TestMeter_CallsCircuitBreakerOutcome_OnSuccessAndError(t *testing.T) {
	var mu sync.Mutex
	var outcomes []error

	outcomeFn := func(err error) {
		mu.Lock()
		outcomes = append(outcomes, err)
		mu.Unlock()
	}

	t.Run("success", func(t *testing.T) {
		mu.Lock()
		outcomes = nil
		mu.Unlock()

		src := feed(providers.StreamChunk{ID: "1"})
		out := Meter(context.Background(), src, time.Now(), MeterMeta{
			Provider:              "openai",
			Model:                 "gpt-4o",
			MetricModel:           "gpt-4o",
			Catalog:               models.Catalog{},
			CircuitBreakerOutcome: outcomeFn,
		})
		for range out { //nolint:revive // empty-block: intentionally draining the stream to completion
		}

		mu.Lock()
		defer mu.Unlock()
		if len(outcomes) != 1 || outcomes[0] != nil {
			t.Fatalf("outcomes = %v, want single nil", outcomes)
		}
	})

	t.Run("provider error", func(t *testing.T) {
		mu.Lock()
		outcomes = nil
		mu.Unlock()

		providerErr := errors.New("provider blew up")
		src := feed(
			providers.StreamChunk{ID: "1"},
			providers.StreamChunk{Error: providerErr},
		)
		out := Meter(context.Background(), src, time.Now(), MeterMeta{
			Provider:              "openai",
			Model:                 "gpt-4o",
			MetricModel:           "gpt-4o",
			Catalog:               models.Catalog{},
			CircuitBreakerOutcome: outcomeFn,
		})
		for range out { //nolint:revive // empty-block: intentionally draining the stream to completion
		}

		mu.Lock()
		defer mu.Unlock()
		if len(outcomes) != 1 {
			t.Fatalf("outcomes len = %d, want 1", len(outcomes))
		}
		if !errors.Is(outcomes[0], providerErr) {
			t.Fatalf("outcome = %v, want provider error", outcomes[0])
		}
	})
}

func TestMeter_CallsCircuitBreakerOutcome_OnAfterPluginError(t *testing.T) {
	var outcomeErr error
	pluginErr := &plugin.RejectionError{Plugin: "after", PluginType: plugin.TypeLogging, Stage: plugin.StageAfterRequest, Reason: "rejected"}
	src := feed(
		providers.StreamChunk{ID: "1", Choices: []providers.StreamChoice{{
			Delta: providers.MessageDelta{Content: "ok"},
		}}},
	)

	out := Meter(context.Background(), src, time.Now(), MeterMeta{
		Provider:    "openai",
		Model:       "gpt-4o",
		MetricModel: "gpt-4o",
		Catalog:     models.Catalog{},
		CompletionFn: func(context.Context, *providers.Response) error {
			return pluginErr
		},
		CircuitBreakerOutcome: func(err error) {
			outcomeErr = err
		},
	})
	for range out { //nolint:revive // empty-block: intentionally draining the stream to completion
	}

	if outcomeErr != nil {
		t.Fatalf("circuit breaker outcome error = %v, want nil after successful provider stream", outcomeErr)
	}
}

func TestMeter_NilPublishFn_ForwardsStream(t *testing.T) {
	src := feed(providers.StreamChunk{ID: "1"})
	out := Meter(context.Background(), src, time.Now(), MeterMeta{
		Provider:    "openai",
		Model:       "gpt-4o",
		MetricModel: "gpt-4o",
		Catalog:     models.Catalog{},
		PublishFn:   nil,
	})

	var got []providers.StreamChunk
	for c := range out {
		got = append(got, c)
	}

	// A nil PublishFn must not drop the stream: the chunk still forwards intact.
	if len(got) != 1 || got[0].ID != "1" {
		t.Fatalf("forwarded %+v, want a single chunk with ID %q", got, "1")
	}
}

// A caller that forgets MetricModel must not silently reintroduce unbounded
// cardinality: the label fails closed to "unknown" instead of echoing the raw,
// client-supplied model. Model itself stays raw for cost lookup and events.
func TestMeterMeta_MetricLabelModelFailsClosed(t *testing.T) {
	tests := []struct {
		name string
		meta MeterMeta
		want string
	}{
		{
			name: "bucketed label is used",
			meta: MeterMeta{Model: "raw-client-model", MetricModel: metrics.UnknownModelLabel},
			want: metrics.UnknownModelLabel,
		},
		{
			name: "known model passes through",
			meta: MeterMeta{Model: "gpt-4o", MetricModel: "gpt-4o"},
			want: "gpt-4o",
		},
		{
			name: "unset MetricModel never leaks the raw model",
			meta: MeterMeta{Model: "raw-client-model"},
			want: metrics.UnknownModelLabel,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.meta.metricLabelModel(); got != tt.want {
				t.Fatalf("metricLabelModel() = %q, want %q", got, tt.want)
			}
			if tt.meta.Model == "" {
				t.Fatal("Model must remain available for cost lookup")
			}
		})
	}
}

// The unset case must not merely default — it must also keep the raw model out
// of the emitted series.
func TestMeter_UnsetMetricModelDoesNotEmitRawModelLabel(t *testing.T) {
	const rawModel = "streamwrap-raw-model-must-not-appear"

	src := feed(providers.StreamChunk{Usage: &providers.Usage{PromptTokens: 1, CompletionTokens: 1}})
	out := Meter(context.Background(), src, time.Now(), MeterMeta{
		Provider: "openai",
		Model:    rawModel, // MetricModel deliberately unset
		Catalog:  models.Catalog{},
	})
	for range out { //nolint:revive // drain
	}

	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "gateway_requests_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, label := range m.GetLabel() {
				if label.GetName() == "model" && label.GetValue() == rawModel {
					t.Fatalf("raw model %q leaked into a metric series", rawModel)
				}
			}
		}
	}
}
