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
//
// The assertion threshold is not "just above the 20ms context timeout":
// every canceled-fetch call falls through to parsing the embedded
// catalog_backup.json (3.3 MB, ~2500 entries) plus rebuilding its lookup
// index, which alone costs ~500ms under `go test -race` (measured directly;
// this is constant per call, independent of the network timing this test
// actually cares about). serverDelay and the assertion threshold are set
// well above that floor so the two are never in the same ballpark.
func TestRefreshCatalog_AbortsOnCanceledContext(t *testing.T) {
	const (
		serverDelay = 5 * time.Second
		wantUnder   = 2 * time.Second
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
