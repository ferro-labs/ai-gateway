package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

func TestOpenAIProvider_Speech_Interface(_ *testing.T) {
	p, _ := New("test-key", "")
	var _ core.SpeechProvider = p
}

func TestOpenAIProvider_Speech_MockHTTP(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/audio/speech") {
			t.Errorf("path = %q, want suffix /audio/speech", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want Bearer test-key", got)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("AUDIOBYTES"))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	resp, err := p.Speech(context.Background(), core.SpeechRequest{
		Model: "tts-1", Input: "hello world", Voice: "alloy", ResponseFormat: "mp3",
	})
	if err != nil {
		t.Fatalf("Speech() error: %v", err)
	}
	if string(resp.Audio) != "AUDIOBYTES" {
		t.Errorf("Audio = %q, want AUDIOBYTES", resp.Audio)
	}
	if resp.ContentType != "audio/mpeg" {
		t.Errorf("ContentType = %q, want audio/mpeg", resp.ContentType)
	}
	if body["model"] != "tts-1" || body["input"] != "hello world" || body["voice"] != "alloy" {
		t.Errorf("body = %v, want model/input/voice forwarded", body)
	}
}

// An omitted response_format is resolved to mp3 on the wire, so the served
// Content-Type (audio/mpeg) matches the bytes even for a wav-defaulting upstream.
func TestOpenAIProvider_Speech_DefaultFormatMP3(t *testing.T) {
	var gotFormat any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		gotFormat = body["response_format"]
		_, _ = w.Write([]byte("x"))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	resp, err := p.Speech(context.Background(), core.SpeechRequest{Model: "tts-1", Input: "hi", Voice: "alloy"})
	if err != nil {
		t.Fatalf("Speech() error: %v", err)
	}
	if gotFormat != "mp3" {
		t.Errorf("response_format on wire = %v, want mp3 (defaulted)", gotFormat)
	}
	if resp.ContentType != "audio/mpeg" {
		t.Errorf("ContentType = %q, want audio/mpeg", resp.ContentType)
	}
}

func TestOpenAIProvider_Speech_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	_, err := p.Speech(context.Background(), core.SpeechRequest{Model: "tts-1", Input: "hi", Voice: "alloy"})
	if err == nil {
		t.Fatal("Speech() error = nil, want 401")
	}
	if core.ParseStatusCode(err) != http.StatusUnauthorized {
		t.Errorf("ParseStatusCode = %d, want 401", core.ParseStatusCode(err))
	}
}
