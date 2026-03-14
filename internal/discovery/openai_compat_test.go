package discovery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDiscoverOpenAICompatibleModels_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("expected Authorization header 'Bearer test-key', got %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"model-1","object":"model","created":1234567890,"owned_by":"acme"},{"id":"model-2","object":"model","created":1234567891,"owned_by":""}]}`))
	}))
	defer srv.Close()

	models, err := DiscoverOpenAICompatibleModels(context.Background(), srv.Client(), srv.URL, "test-key", "test-provider")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	if models[0].ID != "model-1" || models[0].OwnedBy != "acme" {
		t.Errorf("unexpected model[0]: %+v", models[0])
	}
	if models[1].ID != "model-2" || models[1].OwnedBy != "test-provider" {
		t.Errorf("unexpected model[1] owned_by: want %q got %q", "test-provider", models[1].OwnedBy)
	}
}

func TestDiscoverOpenAICompatibleModels_NoAPIKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("expected no Authorization header, got %q", r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
	}))
	defer srv.Close()

	models, err := DiscoverOpenAICompatibleModels(context.Background(), srv.Client(), srv.URL, "", "p")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(models) != 0 {
		t.Errorf("expected empty slice, got %d models", len(models))
	}
}

func TestDiscoverOpenAICompatibleModels_Non200Status(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("Unauthorized"))
	}))
	defer srv.Close()

	_, err := DiscoverOpenAICompatibleModels(context.Background(), srv.Client(), srv.URL, "bad-key", "p")
	if err == nil {
		t.Fatal("expected error for non-200 status, got nil")
	}
}

func TestDiscoverOpenAICompatibleModels_BadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not json`))
	}))
	defer srv.Close()

	_, err := DiscoverOpenAICompatibleModels(context.Background(), srv.Client(), srv.URL, "", "p")
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestDiscoverOpenAICompatibleModels_BadURL(t *testing.T) {
	_, err := DiscoverOpenAICompatibleModels(context.Background(), http.DefaultClient, "://bad-url", "", "p")
	if err == nil {
		t.Fatal("expected error for bad URL, got nil")
	}
}
