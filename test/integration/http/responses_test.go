//go:build integration
// +build integration

package http_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// POST /v1/responses routes by the body's model to that provider and forwards
// the body, and the upstream response comes back verbatim.
func TestResponses_Create_ForwardsToModelProvider(t *testing.T) {
	var gotPath, gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","status":"completed","usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`))
	}))
	defer upstream.Close()

	env := newTestServer(t)
	env.Stub.SetBaseURL(upstream.URL + "/v1")

	body := `{"model":"` + stubModelName + `","input":"hello"}`
	req := newTestRequest(t, "POST", env.Server.URL+"/v1/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testMasterKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/responses: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, b)
	}
	if gotPath != "/v1/responses" {
		t.Errorf("upstream path = %q, want /v1/responses", gotPath)
	}
	if gotAuth != "Bearer stub-key" {
		t.Errorf("upstream auth = %q, want the provider credential", gotAuth)
	}
	got, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(got), `"resp_1"`) {
		t.Errorf("body = %q, want the upstream response object", got)
	}
}

// A streaming create forwards the SSE stream through unchanged (usage extraction
// is unit-tested; here the stream must reach the client intact).
func TestResponses_Create_Streaming(t *testing.T) {
	stream := "event: response.completed\n" +
		`data: {"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}` + "\n\n"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(stream))
	}))
	defer upstream.Close()

	env := newTestServer(t)
	env.Stub.SetBaseURL(upstream.URL + "/v1")

	body := `{"model":"` + stubModelName + `","input":"hello","stream":true}`
	req := newTestRequest(t, "POST", env.Server.URL+"/v1/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testMasterKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	got, _ := io.ReadAll(resp.Body)
	if string(got) != stream {
		t.Errorf("streamed body altered:\n got %q\nwant %q", got, stream)
	}
}

// A bare-id sub-route carries no model, so it forwards to responses_target.
func TestResponses_IDRoute_ForwardsToResponsesTarget(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"id":"resp_9","object":"response","status":"completed"}`))
	}))
	defer upstream.Close()

	env := newTestServer(t, withResponsesTarget("stub"))
	env.Stub.SetBaseURL(upstream.URL + "/v1")

	req := newTestRequest(t, "GET", env.Server.URL+"/v1/responses/resp_9", nil)
	req.Header.Set("Authorization", "Bearer "+testMasterKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /v1/responses/resp_9: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if gotPath != "/v1/responses/resp_9" {
		t.Errorf("upstream path = %q, want /v1/responses/resp_9", gotPath)
	}
}

// With no responses_target configured, the id sub-routes are off (501). The
// create endpoint is unaffected (it routes by model), so this is only the
// id-route path.
func TestResponses_IDRoute_NotConfigured_Returns501(t *testing.T) {
	env := newTestServer(t) // no withResponsesTarget

	req := newTestRequest(t, "GET", env.Server.URL+"/v1/responses/resp_1", nil)
	req.Header.Set("Authorization", "Bearer "+testMasterKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d", resp.StatusCode)
	}
}

func TestResponses_RequiresAuth(t *testing.T) {
	env := newTestServer(t)

	body := `{"model":"` + stubModelName + `","input":"hi"}`
	req := newTestRequest(t, "POST", env.Server.URL+"/v1/responses", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}
