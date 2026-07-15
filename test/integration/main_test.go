//go:build integration
// +build integration

package integration

import (
	"context"
	"flag"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/testutil"
)

var testDSN string

func TestMain(m *testing.M) {
	os.Exit(testutil.RunWithEmbeddedCatalog(func() int {
		flag.Parse()

		if testing.Short() {
			fmt.Println("skipping integration tests (-short flag set)")
			return 0
		}

		startCtx, cancelStart := context.WithTimeout(context.Background(), 3*time.Minute)
		pg, err := testutil.StartPostgres(startCtx)
		cancelStart()
		if err != nil {
			if os.Getenv("CI") != "" {
				fmt.Printf("FAIL: integration tests require Postgres: %v\n", err)
				return 1
			}
			fmt.Printf("skipping integration tests: %v\n", err)
			return 0
		}
		testDSN = pg.DSN

		code := m.Run()
		stopCtx, cancelStop := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancelStop()
		if err := pg.Terminate(stopCtx); err != nil {
			fmt.Printf("FAIL: terminate Postgres test container: %v\n", err)
			if code == 0 {
				return 1
			}
		}
		return code
	}))
}
