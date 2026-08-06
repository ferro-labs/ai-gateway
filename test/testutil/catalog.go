// Package testutil provides shared test helpers.
//
// catalog.go is compiled in the default build (it is a light, dependency-free
// helper used by unit-test TestMains); gateway.go and postgres.go are behind the
// integration build tag so their heavier dependencies (a real gateway,
// testcontainers) stay out of the default build graph.
package testutil

import (
	"fmt"
	"os"

	"github.com/ferro-labs/ai-gateway/models"
)

const embeddedCatalogURL = "file:///ferro-tests-use-embedded-catalog"

// RunWithEmbeddedCatalog runs a test binary with remote catalog loading
// disabled. The invalid non-HTTP URL makes the catalog loader use its embedded
// fallback without performing network I/O.
func RunWithEmbeddedCatalog(run func() int) (code int) {
	previous, existed := os.LookupEnv(models.CatalogURLEnv)
	if err := os.Setenv(models.CatalogURLEnv, embeddedCatalogURL); err != nil {
		fmt.Fprintf(os.Stderr, "set embedded catalog test environment: %v\n", err)
		return 1
	}

	defer func() {
		var err error
		if existed {
			err = os.Setenv(models.CatalogURLEnv, previous)
		} else {
			err = os.Unsetenv(models.CatalogURLEnv)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "restore catalog test environment: %v\n", err)
			if code == 0 {
				code = 1
			}
		}
	}()

	return run()
}
