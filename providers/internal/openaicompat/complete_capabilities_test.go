package openaicompat

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/internal/logging"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// capturingServer returns an httptest server that records the decoded JSON body
// of the request it receives and replies with a minimal valid chat response.
func capturingServer(t *testing.T, captured *map[string]json.RawMessage) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"x","model":"m","choices":[],"usage":{}}`)
	}))
}

// unsupportedParamReq builds a request that mixes a supported param (temperature)
// with one Unsupported for anthropic (seed), so enforcement is observable.
func unsupportedParamReq() core.Request {
	return core.Request{
		Model:       "m",
		Messages:    []core.Message{{Role: core.RoleUser, Content: "hi"}},
		Temperature: core.Ptr(0.5),
		Seed:        core.Ptr(int64(42)),
	}
}

// TestEnforce_WarnForwardsUnsupported verifies the default (warn) mode forwards
// every param unchanged — the shared builder does not drop in warn mode.
func TestEnforce_WarnForwardsUnsupported(t *testing.T) {
	var body map[string]json.RawMessage
	srv := capturingServer(t, &body)
	defer srv.Close()

	_, err := PostChat(context.Background(), ChatParams{
		HTTPClient: srv.Client(),
		URL:        srv.URL,
		Provider:   "anthropic",
		Label:      "anthropic",
		Headers:    map[string]string{"Content-Type": "application/json"},
		// OnUnsupportedParam left at zero value (warn); no context mode set.
	}, unsupportedParamReq())
	if err != nil {
		t.Fatalf("PostChat: %v", err)
	}
	if _, ok := body["seed"]; !ok {
		t.Error("warn mode must forward unsupported seed, but it was dropped")
	}
	if _, ok := body["temperature"]; !ok {
		t.Error("supported temperature must always be forwarded")
	}
}

// TestEnforce_DropOmitsUnsupported verifies drop mode strips the unsupported
// param from the upstream body while keeping supported params.
func TestEnforce_DropOmitsUnsupported(t *testing.T) {
	var body map[string]json.RawMessage
	srv := capturingServer(t, &body)
	defer srv.Close()

	_, err := PostChat(context.Background(), ChatParams{
		HTTPClient:         srv.Client(),
		URL:                srv.URL,
		Provider:           "anthropic",
		Label:              "anthropic",
		Headers:            map[string]string{"Content-Type": "application/json"},
		OnUnsupportedParam: core.UnsupportedParamDrop,
	}, unsupportedParamReq())
	if err != nil {
		t.Fatalf("PostChat: %v", err)
	}
	if _, ok := body["seed"]; ok {
		t.Error("drop mode must omit unsupported seed from the upstream body")
	}
	if _, ok := body["temperature"]; !ok {
		t.Error("drop mode must keep supported temperature")
	}
}

// TestEnforce_DropViaContext verifies the mode also resolves from the request
// context (the gateway-config wiring path) when ChatParams leaves it unset.
func TestEnforce_DropViaContext(t *testing.T) {
	var body map[string]json.RawMessage
	srv := capturingServer(t, &body)
	defer srv.Close()

	ctx := core.WithUnsupportedParamMode(context.Background(), core.UnsupportedParamDrop)
	_, err := PostChat(ctx, ChatParams{
		HTTPClient: srv.Client(),
		URL:        srv.URL,
		Provider:   "anthropic",
		Label:      "anthropic",
		Headers:    map[string]string{"Content-Type": "application/json"},
	}, unsupportedParamReq())
	if err != nil {
		t.Fatalf("PostChat: %v", err)
	}
	if _, ok := body["seed"]; ok {
		t.Error("drop mode from context must omit unsupported seed")
	}
}

// TestEnforce_RejectReturns400 verifies reject mode fails the request with an
// HTTP 400 error that names the offending param and never reaches the provider.
func TestEnforce_RejectReturns400(t *testing.T) {
	var hit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, err := PostChat(context.Background(), ChatParams{
		HTTPClient:         srv.Client(),
		URL:                srv.URL,
		Provider:           "anthropic",
		Label:              "anthropic",
		Headers:            map[string]string{"Content-Type": "application/json"},
		OnUnsupportedParam: core.UnsupportedParamReject,
	}, unsupportedParamReq())
	if err == nil {
		t.Fatal("reject mode must return an error for an unsupported param")
	}
	if code := core.ParseStatusCode(err); code != http.StatusBadRequest {
		t.Errorf("reject error status = %d, want 400", code)
	}
	if hit {
		t.Error("reject mode must not send the request to the provider")
	}
}

// TestEnforce_StreamingDropParity verifies streaming inherits the same
// enforcement, since PostStream and PostChat share newChatRequest.
func TestEnforce_StreamingDropParity(t *testing.T) {
	var body map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	ch, err := PostStream(context.Background(), ChatParams{
		HTTPClient:         srv.Client(),
		URL:                srv.URL,
		Provider:           "anthropic",
		Label:              "anthropic",
		Headers:            map[string]string{"Content-Type": "application/json"},
		OnUnsupportedParam: core.UnsupportedParamDrop,
	}, unsupportedParamReq())
	if err != nil {
		t.Fatalf("PostStream: %v", err)
	}
	var chunks int
	for range ch {
		chunks++
	}
	if chunks != 0 {
		t.Errorf("expected 0 chunks for a [DONE]-only stream, got %d", chunks)
	}
	if _, ok := body["seed"]; ok {
		t.Error("streaming drop mode must omit unsupported seed from the upstream body")
	}
}

// TestEnforce_WarnLogsUnsupported verifies warn mode forwards the unsupported
// param (see TestEnforce_WarnForwardsUnsupported) AND logs it. A warn mode
// that never warns is indistinguishable from having no compatibility mode at
// all, and the drift guard cannot catch that on its own.
func TestEnforce_WarnLogsUnsupported(t *testing.T) {
	var logs bytes.Buffer
	prevLogger := logging.Logger
	logging.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	defer func() { logging.Logger = prevLogger }()

	var body map[string]json.RawMessage
	srv := capturingServer(t, &body)
	defer srv.Close()

	_, err := PostChat(context.Background(), ChatParams{
		HTTPClient: srv.Client(),
		URL:        srv.URL,
		Provider:   "anthropic",
		Label:      "anthropic",
		Headers:    map[string]string{"Content-Type": "application/json"},
		// OnUnsupportedParam left at zero value (warn); no context mode set.
	}, unsupportedParamReq())
	if err != nil {
		t.Fatalf("PostChat: %v", err)
	}
	if _, ok := body["seed"]; !ok {
		t.Error("warn mode must forward unsupported seed, but it was dropped")
	}
	if !strings.Contains(logs.String(), "seed") {
		t.Errorf("warn mode must log the unsupported param, got logs: %q", logs.String())
	}
}

// TestEnforce_NoProfileSkipsWithoutLogging verifies a provider absent from the
// matrix (fireworks, per TestProfileOf_UnknownProviderAllForward) short-circuits
// before the AllParams scan: no log line, even though the request populates a
// param that anthropic's profile marks Unsupported.
func TestEnforce_NoProfileSkipsWithoutLogging(t *testing.T) {
	var logs bytes.Buffer
	prevLogger := logging.Logger
	logging.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	defer func() { logging.Logger = prevLogger }()

	var body map[string]json.RawMessage
	srv := capturingServer(t, &body)
	defer srv.Close()

	_, err := PostChat(context.Background(), ChatParams{
		HTTPClient: srv.Client(),
		URL:        srv.URL,
		Provider:   "fireworks",
		Label:      "fireworks",
		Headers:    map[string]string{"Content-Type": "application/json"},
	}, unsupportedParamReq())
	if err != nil {
		t.Fatalf("PostChat: %v", err)
	}
	if _, ok := body["seed"]; !ok {
		t.Error("provider with no matrix entry must forward seed")
	}
	if logs.Len() != 0 {
		t.Errorf("provider with no matrix entry must not log anything, got: %q", logs.String())
	}
}
