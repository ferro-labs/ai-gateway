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
// wantUnder is deliberately not "just above the 20ms context timeout" or a
// large, loose bound — it is calibrated to sit strictly between the two
// concrete outcomes this test must distinguish:
//
//   - ctx honored (correct): the fetch aborts at the ~20ms context timeout,
//     then every canceled-fetch call falls through to parsing the embedded
//     catalog_backup.json (3.3 MB, ~2500 entries) plus rebuilding its lookup
//     index — a constant ~500ms under `go test -race` (measured directly),
//     independent of the network timing this test cares about. Total: ~520-700ms.
//   - ctx silently ignored (the regression this test guards against, e.g. a
//     future refactor that reverts to context.Background()): fetchRemote's
//     http.Client has its own fixed Timeout: time.Second that applies
//     regardless of ctx, so the fetch instead runs for the full ~1s before
//     falling through to the same ~500ms parse. Total: ~1.5-1.7s.
//
// wantUnder = 1s sits in the gap between those two ranges with margin on
// both sides. A larger serverDelay only guards against the (irrelevant, since
// the client's own 1s Timeout fires first) case of the server responding
// slower than that; it does not by itself distinguish honored vs. ignored
// ctx — do not "fix" flakiness here by loosening wantUnder without first
// checking it still falls strictly below the ~1.5s ignored-ctx floor.
func TestRefreshCatalog_AbortsOnCanceledContext(t *testing.T) {
	const (
		serverDelay = 5 * time.Second
		wantUnder   = 1 * time.Second
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
