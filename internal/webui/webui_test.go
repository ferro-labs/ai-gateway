package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// bundleFS stands in for a real Vite build: an index.html, content-hashed
// assets, and the runtime config file. Tests drive this rather than the
// embedded dist/, whose contents depend on whether anyone has run `make build`
// in the tree — a suite that changes verdict on that is worse than no suite.
func bundleFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":                {Data: []byte("<!doctype html><title>Ferro</title>")},
		"assets/index-AbCd1234.js":  {Data: []byte("console.log(1)")},
		"assets/index-AbCd1234.css": {Data: []byte(".a{}")},
		"config.json":               {Data: []byte(`{"gatewayBaseUrl":""}`)},
		"favicon.ico":               {Data: []byte("\x00")},
	}
}

const acceptHTML = "text/html,application/xhtml+xml,*/*;q=0.8"

func TestHandlerWithBundle(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		target     string
		accept     string
		wantStatus int
		wantCache  string
		wantBody   string
	}{
		{
			name: "root serves the index uncached", method: http.MethodGet, target: "/", accept: acceptHTML,
			wantStatus: http.StatusOK, wantCache: noStoreCacheControl, wantBody: "<!doctype html>",
		},
		{
			// /overview is a React Router destination with no file behind it, so
			// this is the case that makes a hard refresh work.
			name: "browser deep link falls back to the index", method: http.MethodGet, target: "/overview", accept: acceptHTML,
			wantStatus: http.StatusOK, wantCache: noStoreCacheControl, wantBody: "<!doctype html>",
		},
		{
			name: "nested deep link falls back to the index", method: http.MethodGet, target: "/keys/new", accept: acceptHTML,
			wantStatus: http.StatusOK, wantCache: noStoreCacheControl, wantBody: "<!doctype html>",
		},
		{
			// The filename carries a content hash, so a changed file is a
			// changed URL and the year-long lifetime cannot serve stale code.
			name: "hashed asset is immutable", method: http.MethodGet, target: "/assets/index-AbCd1234.js", accept: acceptHTML,
			wantStatus: http.StatusOK, wantCache: immutableCacheControl, wantBody: "console.log(1)",
		},
		{
			// config.json keeps its name across deploys, so caching it would
			// pin a browser to a stale gateway origin.
			name: "config.json is not cached", method: http.MethodGet, target: "/config.json", accept: acceptHTML,
			wantStatus: http.StatusOK, wantCache: noStoreCacheControl, wantBody: "gatewayBaseUrl",
		},
		{
			// An HTML 200 here would turn a client's typo into a JSON parse
			// error somewhere far from the cause.
			name: "api client gets a 404 rather than the shell", method: http.MethodGet, target: "/v1/typo", accept: "application/json",
			wantStatus: http.StatusNotFound,
		},
		{
			name: "client sending no Accept gets a 404", method: http.MethodGet, target: "/v1/typo",
			wantStatus: http.StatusNotFound,
		},
		{
			name: "post is a 404 even from a browser", method: http.MethodPost, target: "/overview", accept: acceptHTML,
			wantStatus: http.StatusNotFound,
		},
	}

	handler := handlerFor(bundleFS(), true)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.target, nil)
			if tt.accept != "" {
				req.Header.Set("Accept", tt.accept)
			}
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantCache != "" {
				if got := rec.Header().Get("Cache-Control"); got != tt.wantCache {
					t.Errorf("Cache-Control = %q, want %q", got, tt.wantCache)
				}
			}
			if tt.wantBody != "" && !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Errorf("body = %q, want it to contain %q", rec.Body.String(), tt.wantBody)
			}
		})
	}
}

// TestHandlerWithoutBundle covers a binary compiled straight from a checkout,
// where nothing but .gitkeep is embedded.
func TestHandlerWithoutBundle(t *testing.T) {
	handler := handlerFor(fstest.MapFS{}, false)

	t.Run("browser gets the placeholder and a 503", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Accept", acceptHTML)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503: a build with no dashboard must not look healthy", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "Dashboard bundle not built") {
			t.Errorf("body = %q, want the placeholder page", rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("Content-Type = %q, want text/html so the markup renders", ct)
		}
	})

	t.Run("api client still gets a 404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/typo", nil)
		req.Header.Set("Accept", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
	})

	t.Run("HEAD sends headers without a body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodHead, "/", nil)
		req.Header.Set("Accept", acceptHTML)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", rec.Code)
		}
		if rec.Body.Len() != 0 {
			t.Errorf("body = %q, want empty for HEAD", rec.Body.String())
		}
	})
}

// TestAvailableMatchesEmbeddedTree pins Available() to the tree this binary was
// compiled from rather than to a fixed value, because that tree differs between
// a plain checkout and one where `make build` has run. What must hold either
// way is that Available() and the handler agree: a true here with no index.html
// would mean the gateway reports a dashboard it cannot serve.
func TestAvailableMatchesEmbeddedTree(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept", acceptHTML)
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, req)

	if Available() && rec.Code != http.StatusOK {
		t.Fatalf("Available() is true but / returned %d, want 200", rec.Code)
	}
	if !Available() && rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("Available() is false but / returned %d, want 503", rec.Code)
	}
}
