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
