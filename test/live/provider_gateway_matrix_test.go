//go:build live

package live_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/config"
	adminmodel "github.com/ferro-labs/ai-gateway/internal/admin/model"
	"github.com/ferro-labs/ai-gateway/internal/admin/repository"
	"github.com/ferro-labs/ai-gateway/internal/httpserver"
	"github.com/ferro-labs/ai-gateway/internal/requestlog"
	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/pkg/metrics"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

type liveMatrixGateway struct {
	server     *httptest.Server
	logs       *requestlog.SQLWriter
	key        *adminmodel.APIKey
	providerID string
}

func newLiveMatrixGateway(t *testing.T) *liveMatrixGateway {
	t.Helper()
	t.Setenv("ALLOW_UNAUTHENTICATED_PROXY", "false")
	t.Setenv(models.CatalogFetchTimeoutEnv, "0")

	providerID := os.Getenv("LIVE_PROVIDER")
	if providerID == "" {
		t.Skip("LIVE_PROVIDER not set; the matrix harness runs one provider at a time")
	}
	entry, ok := providers.GetProviderEntry(providerID)
	if !ok {
		t.Fatal("LIVE_PROVIDER names an unknown provider")
	}
	cfg := providers.ProviderConfigFromEnv(entry)
	if cfg == nil {
		t.Fatalf("%s credential set is incomplete", providerID)
	}
	p, err := entry.Build(cfg)
	if err != nil {
		t.Fatalf("construct %s provider: %s", providerID, liveErrorClass(err))
	}

	declaredModels := make([]string, 0, 3)
	for _, envName := range []string{"LIVE_CHAT_MODEL", "LIVE_EMBED_MODEL", "LIVE_IMAGE_MODEL"} {
		if model := os.Getenv(envName); model != "" {
			declaredModels = append(declaredModels, model)
		}
	}
	if len(declaredModels) == 0 {
		t.Fatal("at least one live model must be set")
	}

	gw, err := aigateway.New(config.Config{
		Strategy:       config.StrategyConfig{Mode: config.ModeSingle},
		RequestTimeout: "120s",
		Targets: []config.Target{{
			VirtualKey: providerID,
			Models:     declaredModels,
		}},
		Plugins: goal20Plugins(),
	})
	if err != nil {
		t.Fatalf("construct gateway: %s", liveErrorClass(err))
	}
	t.Cleanup(func() {
		if err := gw.Close(); err != nil {
			t.Errorf("close gateway: %v", err)
		}
	})

	logs, err := requestlog.NewSQLiteWriter(t.Context(), filepath.Join(t.TempDir(), "requests.db"))
	if err != nil {
		t.Fatalf("create temporary request-log store: %T", err)
	}
	t.Cleanup(func() {
		if err := logs.Close(); err != nil {
			t.Errorf("close request-log store: %v", err)
		}
	})
	gw.SetRequestLogWriter(logs)
	gw.RegisterProvider(p)
	if err := gw.LoadPlugins(); err != nil {
		t.Fatalf("load request logger: %T", err)
	}

	registry := providers.NewRegistry()
	registry.Register(p)
	keyStore := repository.NewKeyStore()
	key, err := keyStore.Create(t.Context(), "live-matrix-"+providerID, []string{adminmodel.ScopeAdmin}, nil)
	if err != nil {
		t.Fatalf("create gateway key: %T", err)
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
		logs,
		logs,
		repository.NewMemoryAuditStore(),
		"",
		nil,
	)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	env := &liveMatrixGateway{server: server, logs: logs, key: key, providerID: providerID}
	env.requireReady(t, declaredModels)
	return env
}

func (e *liveMatrixGateway) requireReady(t *testing.T, models []string) {
	t.Helper()
	resp := goal20Do(t, http.MethodGet, e.server.URL+"/readyz", "", nil) //nolint:bodyclose // body is closed via the liveMatrixReadGatewayJSON helper
	goal20RequireStatus(t, resp, http.StatusOK)
	goal20Close(t, resp.Body)

	resp = goal20Do(t, http.MethodGet, e.server.URL+"/v1/models", e.key.Key, nil)
	goal20RequireStatus(t, resp, http.StatusOK)
	var body struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode model inventory: %T", err)
	}
	_ = resp.Body.Close()
	found := make(map[string]bool, len(models))
	for _, item := range body.Data {
		if item.OwnedBy == e.providerID {
			found[item.ID] = true
		}
	}
	for _, model := range models {
		if !found[model] {
			t.Fatalf("declared model missing from authenticated inventory")
		}
	}
}

func (e *liveMatrixGateway) waitForLog(t *testing.T, traceID string) requestlog.Entry {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		result, err := e.logs.List(t.Context(), requestlog.Query{
			Stage:    "after_request",
			Provider: e.providerID,
		})
		if err != nil {
			t.Fatalf("read temporary request log: %T", err)
		}
		for _, entry := range result.Data {
			if entry.TraceID == traceID {
				if entry.APIKeyID != e.key.ID {
					t.Fatal("request log api_key_id mismatch")
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

func TestLiveMatrix_Gateway(t *testing.T) {
	env := newLiveMatrixGateway(t)
	surface := os.Getenv("LIVE_GATEWAY_SURFACE")
	model := ""
	path := ""
	var payload map[string]any
	switch surface {
	case "chat":
		model = os.Getenv("LIVE_CHAT_MODEL")
		path = "/v1/chat/completions"
		payload = map[string]any{
			"model": model, "messages": []map[string]string{{"role": "user", "content": "Say exactly: pong"}}, "max_tokens": 16,
		}
	case "stream":
		model = os.Getenv("LIVE_CHAT_MODEL")
		path = "/v1/chat/completions"
		payload = map[string]any{
			"model": model, "messages": []map[string]string{{"role": "user", "content": "Count to 3"}}, "max_tokens": 16,
			"stream": true, "stream_options": map[string]bool{"include_usage": true},
		}
	case "embedding":
		model = os.Getenv("LIVE_EMBED_MODEL")
		path = "/v1/embeddings"
		payload = map[string]any{"model": model, "input": "hello world"}
		if inputType := os.Getenv("LIVE_EMBED_INPUT_TYPE"); inputType != "" {
			payload["input_type"] = inputType
		}
	case "image":
		if os.Getenv("ALLOW_IMAGE_CALLS") != "true" {
			t.Fatal("ALLOW_IMAGE_CALLS=true is required")
		}
		model = os.Getenv("LIVE_IMAGE_MODEL")
		path = "/v1/images/generations"
		payload = map[string]any{
			"model": model, "prompt": "A single black dot centered on a plain white background.", "n": 1,
		}
		if size := os.Getenv("LIVE_IMAGE_SIZE"); size != "" {
			payload["size"] = size
		}
		if quality := os.Getenv("LIVE_IMAGE_QUALITY"); quality != "" {
			payload["quality"] = quality
		}
	default:
		t.Fatal("LIVE_GATEWAY_SURFACE is invalid")
	}
	if model == "" {
		t.Fatal("selected surface model is not set")
	}

	handles := metrics.ForRequest(env.providerID, model)
	successBefore := goal20CounterValue(t, handles.Success)
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encode request: %T", err)
	}
	resp := goal20Do(t, http.MethodPost, env.server.URL+path, env.key.Key, bytes.NewReader(body)) //nolint:bodyclose // body is closed via the liveMatrixReadGatewayJSON helper
	goal20RequireStatus(t, resp, http.StatusOK)
	traceID := goal20RequestID(t, resp)

	var outputUnits int
	if surface == "stream" {
		outputUnits = liveMatrixReadGatewayStream(t, resp.Body, model)
	} else {
		outputUnits = liveMatrixReadGatewayJSON(t, resp.Body, surface, env.providerID, model)
	}
	entry := env.waitForLog(t, traceID)
	metricModel := model
	metricDelta := goal20CounterValue(t, handles.Success) - successBefore
	if metricDelta != 1 && entry.Model != "" && entry.Model != model {
		metricModel = entry.Model
		metricDelta = goal20CounterValue(t, metrics.ForRequest(env.providerID, metricModel).Success)
	}
	if metricDelta != 1 {
		t.Fatalf("gateway success metric delta=%v, want 1", metricDelta)
	}
	cost := -1.0
	if entry.CostUSD != nil {
		cost = *entry.CostUSD
	}
	t.Logf("provider=%s surface=%s requested_model=%s logged_model=%s metric_model=%s output_units=%d tokens=%d cost_usd=%.9f correlated=true",
		env.providerID, surface, model, entry.Model, metricModel, outputUnits, entry.TotalTokens, cost)
}

func liveMatrixReadGatewayJSON(t *testing.T, body io.ReadCloser, surface, providerID, _ string) int {
	t.Helper()
	defer func() { _ = body.Close() }()
	switch surface {
	case "chat":
		var resp core.Response
		if err := json.NewDecoder(body).Decode(&resp); err != nil {
			t.Fatalf("decode chat response: %T", err)
		}
		if resp.Provider != providerID || len(resp.Choices) == 0 {
			t.Fatal("chat response attribution or shape mismatch")
		}
		message := resp.Choices[0].Message
		units := len(message.Content) + len(message.ReasoningContent) + len(message.ToolCalls)
		if units == 0 {
			t.Fatal("chat response has no normalized output")
		}
		return units
	case "embedding":
		var resp core.EmbeddingResponse
		if err := json.NewDecoder(body).Decode(&resp); err != nil {
			t.Fatalf("decode embedding response: %T", err)
		}
		if len(resp.Data) == 0 || len(resp.Data[0].Embedding) == 0 {
			t.Fatal("embedding response has no vector")
		}
		return len(resp.Data[0].Embedding)
	case "image":
		var resp core.ImageResponse
		if err := json.NewDecoder(body).Decode(&resp); err != nil {
			t.Fatalf("decode image response: %T", err)
		}
		if len(resp.Data) == 0 {
			t.Fatal("image response has no images")
		}
		units := 0
		for _, item := range resp.Data {
			units += len(item.URL) + len(item.B64JSON)
		}
		if units == 0 {
			t.Fatal("image response has neither URL nor base64 data")
		}
		return units
	default:
		t.Fatal("unsupported JSON surface")
		return 0
	}
}

func liveMatrixReadGatewayStream(t *testing.T, body io.ReadCloser, model string) int {
	t.Helper()
	defer func() { _ = body.Close() }()
	scanner := bufio.NewScanner(body)
	var chunks, units int
	var responseModel string
	sawDone := false
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
			t.Fatalf("decode stream chunk: %T", err)
		}
		if chunk.Model != "" {
			if responseModel == "" {
				responseModel = chunk.Model
			} else if chunk.Model != responseModel {
				t.Fatal("stream changed model identifiers between chunks")
			}
		}
		for _, choice := range chunk.Choices {
			units += len(choice.Delta.Content) + len(choice.Delta.ReasoningContent) + len(choice.Delta.ToolCalls)
		}
		chunks++
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read stream: %T", err)
	}
	if chunks == 0 || units == 0 || !sawDone {
		t.Fatal("stream is missing output or termination invariants")
	}
	t.Logf("requested_model=%s response_model=%s stream_chunks=%d", model, responseModel, chunks)
	return units
}
