package aigateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/models"
)

// TestGateway_RefreshCatalog_AbortsOnCanceledContext proves refreshCatalog
// passes its context to the in-flight remote request.
func TestGateway_RefreshCatalog_AbortsOnCanceledContext(t *testing.T) {
	requestStarted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	t.Setenv(models.CatalogURLEnv, server.URL)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	g := &Gateway{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		g.refreshCatalog(ctx)
	}()

	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("catalog request did not start")
	}
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("refreshCatalog did not return after context cancellation")
	}
}
