//go:build integration
// +build integration

package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// buildAudioMultipart builds a multipart body carrying a model field and an
// audio file, returning the body and its Content-Type header.
func buildAudioMultipart(t *testing.T, model, filename string, audio []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if model != "" {
		_ = mw.WriteField("model", model)
	}
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	_, _ = fw.Write(audio)
	_ = mw.Close()
	return &buf, mw.FormDataContentType()
}

func TestTranscriptions_Success(t *testing.T) {
	env := newTestServer(t)

	body, contentType := buildAudioMultipart(t, stubTranscriptionModel, "clip.mp3", []byte("AUDIO"))
	req := newTestRequest(t, "POST", env.Server.URL+"/v1/audio/transcriptions", body)
	req.Header.Set("Authorization", "Bearer "+testMasterKey)
	req.Header.Set("Content-Type", contentType)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/audio/transcriptions: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, b)
	}
	var result struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Text == "" {
		t.Error("expected non-empty transcript")
	}
}

func TestTranscriptions_MissingModel_Returns400(t *testing.T) {
	env := newTestServer(t)
	body, contentType := buildAudioMultipart(t, "", "clip.mp3", []byte("AUDIO"))
	req := newTestRequest(t, "POST", env.Server.URL+"/v1/audio/transcriptions", body)
	req.Header.Set("Authorization", "Bearer "+testMasterKey)
	req.Header.Set("Content-Type", contentType)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestTranscriptions_CapabilityMiss_Returns404(t *testing.T) {
	env := newTestServer(t)
	body, contentType := buildAudioMultipart(t, "no-such-audio-model", "clip.mp3", []byte("AUDIO"))
	req := newTestRequest(t, "POST", env.Server.URL+"/v1/audio/transcriptions", body)
	req.Header.Set("Authorization", "Bearer "+testMasterKey)
	req.Header.Set("Content-Type", contentType)

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

func TestTranscriptions_UpstreamError_Returns500(t *testing.T) {
	env := newTestServer(t)
	env.Stub.TranscribeHook = func(_ context.Context, _ core.TranscriptionRequest) (*core.TranscriptionResponse, error) {
		return nil, errors.New("upstream audio API error (503)")
	}
	body, contentType := buildAudioMultipart(t, stubTranscriptionModel, "clip.mp3", []byte("AUDIO"))
	req := newTestRequest(t, "POST", env.Server.URL+"/v1/audio/transcriptions", body)
	req.Header.Set("Authorization", "Bearer "+testMasterKey)
	req.Header.Set("Content-Type", contentType)

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

// TestTranslations_RoutesToTranslate confirms /v1/audio/translations sets the
// Translate flag the adapter uses to pick the translations endpoint.
func TestTranslations_RoutesToTranslate(t *testing.T) {
	env := newTestServer(t)
	var gotTranslate bool
	env.Stub.TranscribeHook = func(_ context.Context, req core.TranscriptionRequest) (*core.TranscriptionResponse, error) {
		gotTranslate = req.Translate
		return &core.TranscriptionResponse{Text: "translated"}, nil
	}
	body, contentType := buildAudioMultipart(t, stubTranscriptionModel, "clip.mp3", []byte("AUDIO"))
	req := newTestRequest(t, "POST", env.Server.URL+"/v1/audio/translations", body)
	req.Header.Set("Authorization", "Bearer "+testMasterKey)
	req.Header.Set("Content-Type", contentType)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, b)
	}
	if !gotTranslate {
		t.Error("expected /v1/audio/translations to set Translate=true")
	}
}

func TestTranscriptions_RequiresAuth(t *testing.T) {
	env := newTestServer(t)
	body, contentType := buildAudioMultipart(t, stubTranscriptionModel, "clip.mp3", []byte("AUDIO"))
	req := newTestRequest(t, "POST", env.Server.URL+"/v1/audio/transcriptions", body)
	req.Header.Set("Content-Type", contentType)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}
