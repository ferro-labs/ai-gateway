//go:build live
// +build live

package live_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/config"
	adminmodel "github.com/ferro-labs/ai-gateway/internal/admin/model"
	"github.com/ferro-labs/ai-gateway/internal/admin/repository"
	"github.com/ferro-labs/ai-gateway/internal/httpserver"
	"github.com/ferro-labs/ai-gateway/internal/requestlog"
	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/observability"
	"github.com/ferro-labs/ai-gateway/pkg/metrics"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"
	"github.com/ferro-labs/ai-gateway/providers/groq"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	_ "github.com/ferro-labs/ai-gateway/plugin/logger"
)

type goal20Observer struct {
	observability.Provider

	mu     sync.Mutex
	starts []observability.RequestAttrs
	events []observability.Event
	ended  int
}

func newGoal20Observer() *goal20Observer {
	return &goal20Observer{Provider: observability.NoOp()}
}

func (o *goal20Observer) StartRequestSpan(ctx context.Context, attrs observability.RequestAttrs) (context.Context, observability.Span) {
	o.mu.Lock()
	o.starts = append(o.starts, attrs)
	o.mu.Unlock()

	ctx, span := o.Provider.StartRequestSpan(ctx, attrs)
	return ctx, &goal20Span{Span: span, observer: o}
}

func (o *goal20Observer) RecordEvent(_ context.Context, event observability.Event) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, event)
}

func (*goal20Observer) RecordingEnabled() bool { return true }

func (o *goal20Observer) hasCompletedTrace(traceID string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()

	started := false
	for _, attrs := range o.starts {
		if attrs.TraceID == traceID {
			started = true
			break
		}
	}
	if !started {
		return false
	}
	for _, event := range o.events {
		if event.TraceID == traceID && event.Subject == aigateway.SubjectRequestCompleted &&
			event.Provider == groq.Name && event.Model == groqChat.id && event.Status == http.StatusOK {
			return true
		}
	}
	return false
}

func (o *goal20Observer) endedCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.ended
}

type goal20Span struct {
	observability.Span
	observer *goal20Observer
}

func (s *goal20Span) End() {
	s.Span.End()
	s.observer.mu.Lock()
	s.observer.ended++
	s.observer.mu.Unlock()
}

func goal20Plugins() []config.PluginConfig {
	cfg := map[string]any{"persist": true, "level": "info"}
	return []config.PluginConfig{
		{Name: "request-logger", Type: "logging", Stage: "before_request", Enabled: true, Config: cfg},
		{Name: "request-logger", Type: "logging", Stage: "after_request", Enabled: true, Config: cfg},
		{Name: "request-logger", Type: "logging", Stage: "on_error", Enabled: true, Config: cfg},
	}
}

type goal20Gateway struct {
	server   *httptest.Server
	logs     *requestlog.SQLWriter
	observer *goal20Observer
	key      *adminmodel.APIKey
}

func newGoal20GroqGateway(t *testing.T) *goal20Gateway {
	t.Helper()
	t.Setenv("ALLOW_UNAUTHENTICATED_PROXY", "false")
	t.Setenv(models.CatalogFetchTimeoutEnv, "0")

	p, err := groq.New(requireKey(t, "GROQ_API_KEY"), "")
	if err != nil {
		t.Fatalf("construct Groq provider: %T", err)
	}

	gw, err := aigateway.New(config.Config{
		Strategy:       config.StrategyConfig{Mode: config.ModeSingle},
		RequestTimeout: "30s",
		Targets: []config.Target{{
			VirtualKey: groq.Name,
			Models:     []string{groqChat.id},
		}},
		Plugins: goal20Plugins(),
		Compatibility: config.CompatibilityConfig{
			OnUnsupportedParam: "reject",
		},
	})
	if err != nil {
		t.Fatalf("construct gateway: %v", err)
	}
	t.Cleanup(func() {
		if err := gw.Close(); err != nil {
			t.Errorf("close gateway: %v", err)
		}
	})

	logStore, err := requestlog.NewSQLiteWriter(t.Context(), filepath.Join(t.TempDir(), "requests.db"))
	if err != nil {
		t.Fatalf("create temporary request-log store: %v", err)
	}
	t.Cleanup(func() {
		if err := logStore.Close(); err != nil {
			t.Errorf("close request-log store: %v", err)
		}
	})

	gw.SetRequestLogWriter(logStore)
	gw.RegisterProvider(p)
	observer := newGoal20Observer()
	gw.SetObservability(observer)
	if err := gw.LoadPlugins(); err != nil {
		t.Fatalf("load request logger: %v", err)
	}

	registry := providers.NewRegistry()
	registry.Register(p)
	keyStore := repository.NewKeyStore()
	key, err := keyStore.Create(t.Context(), "goal20-groq-live", []string{adminmodel.ScopeAdmin}, nil)
	if err != nil {
		t.Fatalf("create gateway key: %v", err)
	}
	t.Cleanup(func() {
		if err := keyStore.Revoke(context.Background(), key.ID); err != nil {
			t.Errorf("revoke gateway key: %v", err)
		}
	})

	router := httpserver.NewRouter(
		registry,
		keyStore,
		repository.NewSessionStore(),
		nil,
		gw,
		nil,
		nil,
		logStore,
		logStore,
		repository.NewMemoryAuditStore(),
		"",
		nil,
	)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	return &goal20Gateway{server: server, logs: logStore, observer: observer, key: key}
}

func goal20Do(t *testing.T, method, url, key string, body io.Reader) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, url, body)
	if err != nil {
		t.Fatalf("create gateway request: %v", err)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("gateway request failed: %T", err)
	}
	return resp
}

func goal20Close(t *testing.T, body io.ReadCloser) {
	t.Helper()
	_, _ = io.Copy(io.Discard, body)
	if err := body.Close(); err != nil {
		t.Errorf("close response body: %v", err)
	}
}

func goal20RequireStatus(t *testing.T, resp *http.Response, want int) {
	t.Helper()
	if resp.StatusCode != want {
		goal20Close(t, resp.Body)
		t.Fatalf("gateway status=%d, want %d", resp.StatusCode, want)
	}
}

func goal20RequestID(t *testing.T, resp *http.Response) string {
	t.Helper()
	id := resp.Header.Get("X-Request-ID")
	if len(id) != 32 {
		t.Fatalf("X-Request-ID has invalid shape")
	}
	for _, r := range id {
		if !strings.ContainsRune("0123456789abcdef", r) {
			t.Fatalf("X-Request-ID has invalid shape")
		}
	}
	return id
}

func goal20WaitForLog(t *testing.T, store *requestlog.SQLWriter, traceID, keyID string) requestlog.Entry {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		result, err := store.List(t.Context(), requestlog.Query{
			Stage:    "after_request",
			Model:    groqChat.id,
			Provider: groq.Name,
		})
		if err != nil {
			t.Fatalf("read temporary request log: %v", err)
		}
		for _, entry := range result.Data {
			if entry.TraceID == traceID {
				if entry.APIKeyID != keyID {
					t.Fatalf("request log api_key_id mismatch")
				}
				if entry.TotalTokens <= 0 || entry.CostUSD == nil || *entry.CostUSD <= 0 {
					t.Fatalf("request log is missing usage or cost attribution")
				}
				return entry
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("request log row was not correlated to X-Request-ID")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func goal20WaitForObservation(t *testing.T, observer *goal20Observer, traceID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !observer.hasCompletedTrace(traceID) {
		if time.Now().After(deadline) {
			t.Fatal("span/event was not correlated to X-Request-ID")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func goal20CounterValue(t *testing.T, counter prometheus.Counter) float64 {
	t.Helper()
	var metric dto.Metric
	if err := counter.Write(&metric); err != nil {
		t.Fatalf("read Prometheus counter: %v", err)
	}
	return metric.GetCounter().GetValue()
}

//nolint:gocyclo,maintidx // one end-to-end matrix over every gateway surface; splitting it would hide the ordering it asserts
func TestLive_Groq_GatewayHTTP(t *testing.T) {
	env := newGoal20GroqGateway(t)
	baseURL := env.server.URL

	for _, path := range []string{"/health", "/livez"} {
		resp := goal20Do(t, http.MethodGet, baseURL+path, "", nil) //nolint:bodyclose // body is closed via the goal20Close helper, which bodyclose cannot follow
		goal20RequireStatus(t, resp, http.StatusOK)
		goal20Close(t, resp.Body)
	}

	ready := goal20Do(t, http.MethodGet, baseURL+"/readyz", "", nil)
	goal20RequireStatus(t, ready, http.StatusOK)
	var readyBody struct {
		Status  string `json:"status"`
		Targets []struct {
			Name     string `json:"name"`
			Routable bool   `json:"routable"`
		} `json:"targets"`
	}
	if err := json.NewDecoder(ready.Body).Decode(&readyBody); err != nil {
		t.Fatalf("decode readyz: %v", err)
	}
	_ = ready.Body.Close()
	if readyBody.Status != "ready" || len(readyBody.Targets) != 1 ||
		readyBody.Targets[0].Name != groq.Name || !readyBody.Targets[0].Routable {
		t.Fatal("readyz did not report the isolated Groq target as routable")
	}

	modelResp := goal20Do(t, http.MethodGet, baseURL+"/v1/models", env.key.Key, nil)
	goal20RequireStatus(t, modelResp, http.StatusOK)
	var modelList struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.NewDecoder(modelResp.Body).Decode(&modelList); err != nil {
		t.Fatalf("decode model inventory: %v", err)
	}
	_ = modelResp.Body.Close()
	modelFound := false
	for _, item := range modelList.Data {
		if item.ID == groqChat.id && item.OwnedBy == groq.Name {
			modelFound = true
			break
		}
	}
	if !modelFound {
		t.Fatal("approved Groq model missing from authenticated inventory")
	}

	capResp := goal20Do(t, http.MethodGet, baseURL+"/v1/capabilities", env.key.Key, nil)
	goal20RequireStatus(t, capResp, http.StatusOK)
	var capabilityBody struct {
		Providers map[string]map[string]string `json:"providers"`
	}
	if err := json.NewDecoder(capResp.Body).Decode(&capabilityBody); err != nil {
		t.Fatalf("decode capability inventory: %v", err)
	}
	_ = capResp.Body.Close()
	if capabilityBody.Providers[groq.Name]["presence_penalty"] != "unsupported" {
		t.Fatal("Groq unsupported-parameter inventory is inconsistent")
	}

	rejectPayload, _ := json.Marshal(map[string]any{
		"model":            groqChat.id,
		"messages":         []map[string]string{{"role": "user", "content": "hi"}},
		"max_tokens":       16,
		"presence_penalty": 0.1,
	})
	rejectResp := goal20Do(t, http.MethodPost, baseURL+"/v1/chat/completions", env.key.Key, bytes.NewReader(rejectPayload))
	goal20RequireStatus(t, rejectResp, http.StatusBadRequest)
	var rejectBody struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(rejectResp.Body).Decode(&rejectBody); err != nil {
		t.Fatalf("decode unsupported-parameter response: %v", err)
	}
	_ = rejectResp.Body.Close()
	if rejectBody.Error.Code != "unsupported_parameter" {
		t.Fatal("unsupported parameter did not use the stable error class")
	}

	handles := metrics.ForRequest(groq.Name, groqChat.id)
	successBefore := goal20CounterValue(t, handles.Success)
	costBefore := goal20CounterValue(t, handles.CostUSD)

	chatPayload, _ := json.Marshal(map[string]any{
		"model":      groqChat.id,
		"messages":   []map[string]string{{"role": "user", "content": "Say exactly: pong"}},
		"max_tokens": 16,
	})
	chatResp := goal20Do(t, http.MethodPost, baseURL+"/v1/chat/completions", env.key.Key, bytes.NewReader(chatPayload))
	goal20RequireStatus(t, chatResp, http.StatusOK)
	chatTraceID := goal20RequestID(t, chatResp)
	var chat core.Response
	if err := json.NewDecoder(chatResp.Body).Decode(&chat); err != nil {
		t.Fatalf("decode chat response: %v", err)
	}
	_ = chatResp.Body.Close()
	if chat.Provider != groq.Name || chat.Model != groqChat.id || len(chat.Choices) == 0 {
		t.Fatal("chat response attribution or shape mismatch")
	}
	chatOutputBytes := len(chat.Choices[0].Message.Content) + len(chat.Choices[0].Message.ReasoningContent)
	if chatOutputBytes == 0 || chat.Usage.TotalTokens <= 0 {
		t.Fatal("chat response is missing normalized output or usage")
	}
	chatLog := goal20WaitForLog(t, env.logs, chatTraceID, env.key.ID)
	goal20WaitForObservation(t, env.observer, chatTraceID)

	streamPayload, _ := json.Marshal(map[string]any{
		"model":          groqChat.id,
		"messages":       []map[string]string{{"role": "user", "content": "Count to 3"}},
		"max_tokens":     16,
		"stream":         true,
		"stream_options": map[string]bool{"include_usage": true},
	})
	streamResp := goal20Do(t, http.MethodPost, baseURL+"/v1/chat/completions", env.key.Key, bytes.NewReader(streamPayload))
	goal20RequireStatus(t, streamResp, http.StatusOK)
	streamTraceID := goal20RequestID(t, streamResp)
	if !strings.HasPrefix(streamResp.Header.Get("Content-Type"), "text/event-stream") {
		_ = streamResp.Body.Close()
		t.Fatal("stream response has an invalid content type")
	}

	var streamChunks, streamOutputBytes, streamTotalTokens int
	var sawDone, sawFinish bool
	scanner := bufio.NewScanner(streamResp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "data: [DONE]" {
			sawDone = true
			break
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var chunk core.StreamChunk
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk); err != nil {
			t.Fatalf("decode stream chunk: %v", err)
		}
		if chunk.Model != "" && chunk.Model != groqChat.id {
			t.Fatal("stream echoed an unexpected model")
		}
		for _, choice := range chunk.Choices {
			streamOutputBytes += len(choice.Delta.Content) + len(choice.Delta.ReasoningContent)
			if choice.FinishReason != "" {
				sawFinish = true
			}
		}
		if chunk.Usage != nil {
			streamTotalTokens = chunk.Usage.TotalTokens
		}
		streamChunks++
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read stream: %v", err)
	}
	_ = streamResp.Body.Close()
	if streamChunks == 0 || streamOutputBytes == 0 || !sawDone || !sawFinish || streamTotalTokens <= 0 {
		t.Fatal("stream is missing output, termination, or usage invariants")
	}
	streamLog := goal20WaitForLog(t, env.logs, streamTraceID, env.key.ID)
	goal20WaitForObservation(t, env.observer, streamTraceID)

	if got := goal20CounterValue(t, handles.Success) - successBefore; got != 2 {
		t.Fatalf("gateway success metric delta=%v, want 2", got)
	}
	if got := goal20CounterValue(t, handles.CostUSD) - costBefore; got <= 0 {
		t.Fatal("gateway cost metric did not increase")
	}
	if env.observer.endedCount() < 3 {
		t.Fatal("gateway request spans were not ended")
	}

	metricsResp := goal20Do(t, http.MethodGet, baseURL+"/metrics", env.key.Key, nil)
	goal20RequireStatus(t, metricsResp, http.StatusOK)
	metricBody, err := io.ReadAll(metricsResp.Body)
	if err != nil {
		t.Fatalf("read metrics response: %v", err)
	}
	_ = metricsResp.Body.Close()
	metricText := string(metricBody)
	if !strings.Contains(metricText, "gateway_requests_total") ||
		!strings.Contains(metricText, `provider="groq"`) ||
		!strings.Contains(metricText, `model="openai/gpt-oss-20b"`) {
		t.Fatal("authenticated metrics output is missing Groq attribution")
	}

	t.Logf(
		"gateway_http: chat_output_bytes=%d stream_output_bytes=%d stream_chunks=%d chat_tokens=%d stream_tokens=%d chat_cost_usd=%.9f stream_cost_usd=%.9f",
		chatOutputBytes,
		streamOutputBytes,
		streamChunks,
		chatLog.TotalTokens,
		streamLog.TotalTokens,
		*chatLog.CostUSD,
		*streamLog.CostUSD,
	)
}
