package aigateway

import "testing"

// TestNew_ValidatesConfig verifies that New() runs the same fail-fast validation
// that ReloadConfig already applies, so callers get a clear error at construction
// time rather than a confusing failure at request time.
func TestNew_ValidatesConfig(t *testing.T) {
	t.Run("empty config (no targets) returns error", func(t *testing.T) {
		_, err := newTestGateway(t, Config{})
		if err == nil {
			t.Fatal("expected New(Config{}) to return an error, got nil")
		}
	})

	t.Run("minimal valid config constructs without error", func(t *testing.T) {
		gw, err := newTestGateway(t, Config{
			Strategy: StrategyConfig{Mode: ModeSingle},
			Targets:  []Target{{VirtualKey: "any"}},
		})

		if err != nil {
			t.Fatalf("expected nil error for valid config, got: %v", err)
		}
		if gw == nil {
			t.Fatal("expected non-nil gateway")
		}
		if err := gw.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})
}
