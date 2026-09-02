package config

import (
	"strings"
	"testing"
)

// TestValidateStrategy_Sticky pins strategy.sticky: only loadbalance and
// ab-test can pin a start target, `on` is a closed set, and a TTL is a
// positive duration.
func TestValidateStrategy_Sticky(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{
			name: "sticky on user under loadbalance is legal",
			cfg: Config{
				Strategy: StrategyConfig{Mode: ModeLoadBalance, Sticky: &StickyConfig{On: StickyOnUser, TTL: "1h"}},
				Targets:  []Target{{VirtualKey: "openai", Weight: 1}, {VirtualKey: "groq", Weight: 1}},
			},
		},
		{
			name: "sticky under fallback is refused",
			cfg: Config{
				Strategy: StrategyConfig{Mode: ModeFallback, Sticky: &StickyConfig{On: StickyOnUser}},
				Targets:  twoTargets(),
			},
			wantErr: "sticky applies to loadbalance and ab-test only",
		},
		{
			name: "sticky on an unsupported key is refused",
			cfg: Config{
				Strategy: StrategyConfig{Mode: ModeLoadBalance, Sticky: &StickyConfig{On: "session"}},
				Targets:  []Target{{VirtualKey: "openai", Weight: 1}, {VirtualKey: "groq", Weight: 1}},
			},
			wantErr: "sticky.on must be \"user\"",
		},
		{
			name: "sticky ttl must be a positive duration",
			cfg: Config{
				Strategy: StrategyConfig{Mode: ModeLoadBalance, Sticky: &StickyConfig{On: StickyOnUser, TTL: "soon"}},
				Targets:  []Target{{VirtualKey: "openai", Weight: 1}, {VirtualKey: "groq", Weight: 1}},
			},
			wantErr: "is not a duration",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateConfig(tc.cfg)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateConfig = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("ValidateConfig = %v, want an error containing %q", err, tc.wantErr)
			}
		})
	}
}
