package aigateway

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/ferro-labs/ai-gateway/internal/testutil"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	code := testutil.RunWithEmbeddedCatalog(func() int {
		previousLogger := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
		defer slog.SetDefault(previousLogger)

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

func newTestGateway(tb testing.TB, cfg Config) (*Gateway, error) {
	tb.Helper()
	gw, err := New(cfg)
	if err == nil {
		tb.Cleanup(func() {
			if closeErr := gw.Close(); closeErr != nil {
				tb.Errorf("close gateway: %v", closeErr)
			}
		})
	}
	return gw, err
}
