//go:build integration
// +build integration

package plugins_test

import (
	"os"
	"testing"

	"github.com/ferro-labs/ai-gateway/internal/testutil"
)

func TestMain(m *testing.M) {
	os.Exit(testutil.RunWithEmbeddedCatalog(m.Run))
}
