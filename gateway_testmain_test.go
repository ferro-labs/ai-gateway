package aigateway

import (
	"fmt"
	"io"
	"os"
	"testing"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/pkg/logger"
	"github.com/ferro-labs/ai-gateway/test/testutil"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	code := testutil.RunWithEmbeddedCatalog(func() int {
		previousLogger := logger.Default()
		logger.SetDefault(logger.New(logger.Options{Output: io.Discard}))
		defer logger.SetDefault(previousLogger)

		ignoreCurrent := goleak.IgnoreCurrent()
		code := m.Run()
		if err := goleak.Find(ignoreCurrent); err != nil {
			fmt.Fprintf(os.Stderr, "gateway tests leaked goroutines: %v\n", err)
			return 1
		}
		return code
	})
	os.Exit(code)
}

func newTestGateway(tb testing.TB, cfg config.Config, opts ...Option) (*Gateway, error) {
	tb.Helper()
	gw, err := New(cfg, opts...)
	if err == nil {
		tb.Cleanup(func() {
			if closeErr := gw.Close(); closeErr != nil {
				tb.Errorf("close gateway: %v", closeErr)
			}
		})
	}
	return gw, err
}
