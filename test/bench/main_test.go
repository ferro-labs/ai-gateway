package bench

import (
	"os"
	"testing"

	"github.com/ferro-labs/ai-gateway/test/testutil"
)

// TestMain forces the embedded model catalog so the gateway benchmarks build a
// gateway without any network catalog fetch at startup.
func TestMain(m *testing.M) {
	os.Exit(testutil.RunWithEmbeddedCatalog(m.Run))
}
