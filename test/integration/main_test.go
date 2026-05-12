//go:build integration
// +build integration

package integration

import (
	"flag"
	"fmt"
	"os"
	"testing"

	"github.com/ferro-labs/ai-gateway/internal/testutil"
)

var testDSN string

func TestMain(m *testing.M) {
	flag.Parse()

	if testing.Short() {
		fmt.Println("skipping integration tests (-short flag set)")
		os.Exit(0)
	}

	pg, err := testutil.StartPostgres()
	if err != nil {
		if os.Getenv("CI") != "" {
			fmt.Printf("FAIL: integration tests require Postgres: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("skipping integration tests: %v\n", err)
		os.Exit(0)
	}
	testDSN = pg.DSN

	code := m.Run()
	pg.Terminate()
	os.Exit(code)
}
