//go:build integration
// +build integration

package http_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

func TestSpeech_Success_ReturnsAudioBytes(t *testing.T) {
	env := newTestServer(t)

	body := `{"model":"` + stubSpeechModel + `","input":"hello world","voice":"alloy"}`
	req := newTestRequest(t, "POST", env.Server.URL+"/v1/audio/speech", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+testMasterKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/audio/speech: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, b)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "audio/mpeg" {
		t.Errorf("Content-Type = %q, want audio/mpeg", ct)
	}
	got, _ := io.ReadAll(resp.Body)
	if string(got) != "stub-audio:hello world" {
		t.Errorf("body = %q, want stub-audio:hello world", got)
	}
}

func TestSpeech_MissingInput_Returns400(t *testing.T) {
	env := newTestServer(t)

	body := `{"model":"` + stubSpeechModel + `","voice":"alloy"}`
	req := newTestRequest(t, "POST", env.Server.URL+"/v1/audio/speech", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+testMasterKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

// A model no registered SpeechProvider serves returns 404, not 500.
func TestSpeech_CapabilityMiss_Returns404(t *testing.T) {
	env := newTestServer(t)

	body := `{"model":"no-such-speech-model","input":"hi","voice":"alloy"}`
	req := newTestRequest(t, "POST", env.Server.URL+"/v1/audio/speech", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+testMasterKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	if resp.StatusCode != http.StatusNotFound {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 404, got %d: %s", resp.StatusCode, b)
	}
	assertOpenAIError(t, resp.Body, "invalid_request_error", "model_not_found")
}

func TestSpeech_UpstreamError_Returns500(t *testing.T) {
	env := newTestServer(t)
	env.Stub.SpeechHook = func(_ context.Context, _ core.SpeechRequest) (*core.SpeechResponse, error) {
		return nil, errors.New("upstream speech API error (503)")
	}

	body := `{"model":"` + stubSpeechModel + `","input":"hi","voice":"alloy"}`
	req := newTestRequest(t, "POST", env.Server.URL+"/v1/audio/speech", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+testMasterKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	if resp.StatusCode != http.StatusInternalServerError {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 500, got %d: %s", resp.StatusCode, b)
	}
}

func TestSpeech_RequiresAuth(t *testing.T) {
	env := newTestServer(t)

	body := `{"model":"` + stubSpeechModel + `","input":"hi","voice":"alloy"}`
	req := newTestRequest(t, "POST", env.Server.URL+"/v1/audio/speech", bytes.NewBufferString(body))
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
