package handler

import (
	"os"
	"testing"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/internal/testutil"
)

func TestMain(m *testing.M) {
	os.Exit(testutil.RunWithEmbeddedCatalog(m.Run))
}

func newTestGateway(t *testing.T, cfg aigateway.Config) (*aigateway.Gateway, error) {
	t.Helper()
	gw, err := aigateway.New(cfg)
	if err == nil {
		t.Cleanup(func() {
			if closeErr := gw.Close(); closeErr != nil {
				t.Errorf("close gateway: %v", closeErr)
			}
		})
	}
	return gw, err
}
