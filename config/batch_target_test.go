package config

import (
	"strings"
	"testing"
)

func TestValidateBatchTarget(t *testing.T) {
	tests := []struct {
		name    string
		target  string
		wantErr string
	}{
		{name: "empty is allowed (surface off)", target: ""},
		{name: "names a configured target", target: "openai"},
		{name: "unknown target", target: "nope", wantErr: `batch_target "nope" does not name any configured target`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{
				Strategy:    StrategyConfig{Mode: ModeSingle},
				Targets:     []Target{{VirtualKey: "openai"}, {VirtualKey: "groq"}},
				BatchTarget: tt.target,
			}
			err := ValidateConfig(cfg)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("ValidateConfig() = %v, want nil", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("ValidateConfig() = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateResponsesTarget(t *testing.T) {
	base := Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "openai"}, {VirtualKey: "xai"}},
	}

	base.ResponsesTarget = "xai"
	if err := ValidateConfig(base); err != nil {
		t.Fatalf("ValidateConfig(responses_target=xai) = %v, want nil", err)
	}

	base.ResponsesTarget = "nope"
	err := ValidateConfig(base)
	if err == nil || !strings.Contains(err.Error(), `responses_target "nope" does not name any configured target`) {
		t.Fatalf("ValidateConfig(responses_target=nope) = %v, want naming error", err)
	}
}
