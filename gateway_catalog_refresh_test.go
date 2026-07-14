package aigateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/models"
)

// TestRefreshCatalog_AbortsOnCanceledContext proves refreshCatalog's ctx
// parameter is actually wired into the fetch (not just accepted and
// ignored): with a context that expires well before a slow upstream
// responds, refreshCatalog must return promptly instead of waiting for the
// server or for fetchRemote's own 1s client timeout. This is the concrete
// regression test for the shutdown-latency gap that motivated threading
// g.shutdownCtx through the periodic catalog refresh.
func TestRefreshCatalog_AbortsOnCanceledContext(t *testing.T) {
	const (
		// serverDelay is large enough that the server never actually
		// responds in time; only fetchRemote's own timing determines elapsed.
		serverDelay = 5 * time.Second
		// wantUnder sits strictly between the two outcomes this test
		// distinguishes: ctx honored aborts at the ~20ms context timeout,
		// then falls through to parsing the embedded catalog_backup.json
		// (~500ms under `go test -race`, constant regardless of network
		// timing) for a ~520-700ms total. ctx silently ignored instead runs
		// into fetchRemote's own fixed 1s client Timeout before the same
		// ~500ms parse, for a ~1.5-1.7s total. 1s has margin on both sides.
		wantUnder = 1 * time.Second
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(serverDelay)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"test/remote":{"provider":"test","model_id":"remote","mode":"chat"}}`))
	}))
	t.Cleanup(server.Close)
	t.Setenv(models.CatalogURLEnv, server.URL)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()

	g := &Gateway{}
	start := time.Now()
	g.refreshCatalog(ctx)
	elapsed := time.Since(start)

	if elapsed >= wantUnder {
		t.Fatalf("refreshCatalog took %v, want under %v (ctx cancellation must abort the fetch early; the %v server delay is never actually waited out)", elapsed, wantUnder, serverDelay)
	}
}
