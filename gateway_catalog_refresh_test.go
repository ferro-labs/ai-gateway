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
	const serverDelay = 500 * time.Millisecond
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

	if elapsed >= serverDelay {
		t.Fatalf("refreshCatalog took %v, want well under the server's %v delay (ctx cancellation must abort the fetch early)", elapsed, serverDelay)
	}
}
