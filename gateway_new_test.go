package aigateway

import "testing"

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
