//go:build integration
// +build integration

package testutil

import (
	"testing"

	aigateway "github.com/ferro-labs/ai-gateway"
)

// NewTestGateway builds a Gateway from cfg and, on success, registers Close as a
// test cleanup. The integration sub-packages share it so gateway teardown is
// defined once rather than copied per package.
func NewTestGateway(t *testing.T, cfg aigateway.Config) (*aigateway.Gateway, error) {
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
