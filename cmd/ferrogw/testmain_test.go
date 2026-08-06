package main

import (
	"os"
	"testing"

	"github.com/ferro-labs/ai-gateway/test/testutil"
)

func TestMain(m *testing.M) {
	os.Exit(testutil.RunWithEmbeddedCatalog(m.Run))
}
