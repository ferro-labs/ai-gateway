package core

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

func TestDiscoverModelsWithHeaders_WrappedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"m-1","owned_by":"acme"},{"id":"m-2"}]}`))
	}))
	defer srv.Close()

	models, err := DiscoverModelsWithHeaders(context.Background(), srv.Client(), srv.URL, nil, "fallback")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	if models[0].ID != "m-1" || models[0].OwnedBy != "acme" {
		t.Errorf("unexpected model[0]: %+v", models[0])
	}
	if models[1].OwnedBy != "fallback" {
		t.Errorf("model[1] owned_by = %q, want fallback", models[1].OwnedBy)
	}
}

func TestDiscoverModelsWithHeaders_BareArrayBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[{"id":"bare-1"},{"id":"bare-2","owned_by":"x"}]`))
	}))
	defer srv.Close()

	models, err := DiscoverModelsWithHeaders(context.Background(), srv.Client(), srv.URL, nil, "fallback")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	if models[0].ID != "bare-1" || models[0].OwnedBy != "fallback" {
		t.Errorf("unexpected model[0]: %+v", models[0])
	}
	if models[1].ID != "bare-2" || models[1].OwnedBy != "x" {
		t.Errorf("unexpected model[1]: %+v", models[1])
	}
}

func TestDiscoverModelsWithHeaders_SendsCustomHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "secret" {
			t.Errorf("expected x-api-key 'secret', got %q", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("expected anthropic-version '2023-06-01', got %q", r.Header.Get("anthropic-version"))
		}
		if r.Header.Get("Authorization") != "" {
			t.Errorf("expected no Authorization header, got %q", r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"id":"m-1"}]}`))
	}))
	defer srv.Close()

	headers := map[string]string{"x-api-key": "secret", "anthropic-version": "2023-06-01"}
	models, err := DiscoverModelsWithHeaders(context.Background(), srv.Client(), srv.URL, headers, "p")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(models) != 1 || models[0].ID != "m-1" {
		t.Errorf("unexpected models: %+v", models)
	}
}

func TestDiscoverModelsWithHeaders_Non200Status(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("forbidden"))
	}))
	defer srv.Close()

	_, err := DiscoverModelsWithHeaders(context.Background(), srv.Client(), srv.URL, nil, "p")
	if err == nil {
		t.Fatal("expected error for non-200 status, got nil")
	}
}

// TestDiscoverOpenAICompatibleModels_PreservesRetryAfter verifies a 429
// response's Retry-After header survives discovery errors as a typed
// HTTPStatusError so the fallback strategy can honor the provider's own
// backoff hint instead of guessing.
func TestDiscoverOpenAICompatibleModels_PreservesRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "5")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	defer srv.Close()

	_, err := DiscoverOpenAICompatibleModels(context.Background(), srv.Client(), srv.URL, "test-key", "test-provider")
	if err == nil {
		t.Fatal("expected error for 429 status, got nil")
	}
	if got := ParseStatusCode(err); got != http.StatusTooManyRequests {
		t.Errorf("ParseStatusCode(err) = %d, want 429", got)
	}
	if got := RetryAfterFrom(err); got != 5*time.Second {
		t.Errorf("RetryAfterFrom(err) = %v, want 5s", got)
	}
}

// TestDiscoverModelsWithHeaders_BoundsResponseBody verifies the discovery
// response is read through the shared cap rather than io.ReadAll. A /models
// endpoint is an unauthenticated-to-us third party as far as body size goes —
// a proxy, a compromised host, or a misconfigured server can answer it with an
// arbitrarily large body, and reading that whole body into memory is the one
// path in this package that had no bound.
func TestDiscoverModelsWithHeaders_BoundsResponseBody(t *testing.T) {
	const oversized = MaxProviderResponseBytes + 1
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// A body that is syntactically a model list and far too large to hold.
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"`))
		chunk := bytes.Repeat([]byte("a"), 1<<20)
		for written := 0; written < oversized; written += len(chunk) {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	models, err := DiscoverModelsWithHeaders(context.Background(), srv.Client(), srv.URL, nil, "test-provider")
	if err == nil {
		t.Fatalf("expected an error for an oversized discovery body, got %d models", len(models))
	}
	if !strings.Contains(err.Error(), "byte limit") {
		t.Errorf("error = %q, want the response-body cap to be the reason", err.Error())
	}
}

// TestDiscoverModelsRedirectErrorNamesTarget pins REG-004 for model discovery.
//
// A refused redirect used to reduce to "provider: HTTP 302": upstreamMessage
// recovers nothing from the empty or HTML body a redirect carries, so the
// operator was left with a bare status and no way to tell a mistyped
// <PROVIDER>_BASE_URL apart from a host they never configured.
func TestDiscoverModelsRedirectErrorNamesTarget(t *testing.T) {
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer elsewhere.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+r.URL.Path, http.StatusMovedPermanently)
	}))
	defer srv.Close()

	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	_, err := DiscoverOpenAICompatibleModels(context.Background(), client, srv.URL+"/v1/models", "k", "acme")
	if err == nil {
		t.Fatal("discovery succeeded against a redirecting endpoint")
	}
	target := strings.TrimPrefix(elsewhere.URL, "http://")
	if !strings.Contains(err.Error(), target) {
		t.Errorf("error %q does not name the redirect target host %q", err, target)
	}
	if !strings.Contains(err.Error(), "301") {
		t.Errorf("error %q does not carry the status", err)
	}
}
