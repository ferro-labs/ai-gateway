package handlers

import (
	"os"
	"testing"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/test/testutil"
)

func TestMain(m *testing.M) {
	os.Exit(testutil.RunWithEmbeddedCatalog(m.Run))
}

func newTestGateway(t *testing.T, cfg config.Config) (*aigateway.Gateway, error) {
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

func singleConfig() config.Config {
	return config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "openai"}},
	}
}
