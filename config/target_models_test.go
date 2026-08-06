package config

import (
	"strings"
	"testing"
)

// TestValidateTargetModels covers the declared-model list a target may carry.
// Everything here is decided from the config document alone, so ValidateConfig
// answers it and `ferrogw validate` rejects it without credentials or network.
func TestValidateTargetModels(t *testing.T) {
	tests := []struct {
		name    string
		models  []string
		wantErr string
	}{
		{
			name:   "omitted",
			models: nil,
		},
		{
			name:   "empty list",
			models: []string{},
		},
		{
			name:   "single id",
			models: []string{"gemini-2.5-flash"},
		},
		{
			name:   "several ids",
			models: []string{"gemini-2.5-flash", "gemini-2.5-pro", "command-a-03-2025"},
		},
		{
			name:   "id with dots and slashes",
			models: []string{"accounts/fireworks/models/llama-v3p1-8b", "gpt-4.1-mini"},
		},
		{
			name:    "empty string",
			models:  []string{"gemini-2.5-flash", ""},
			wantErr: `models[1] must be a non-empty model id`,
		},
		{
			name:    "whitespace only",
			models:  []string{"   "},
			wantErr: `models[0] must be a non-empty model id`,
		},
		{
			name:    "leading whitespace",
			models:  []string{" gemini-2.5-flash"},
			wantErr: `models[0] must be a non-empty model id`,
		},
		{
			name:    "trailing whitespace",
			models:  []string{"gemini-2.5-flash "},
			wantErr: `models[0] must be a non-empty model id`,
		},
		{
			name:    "trailing newline",
			models:  []string{"gemini-2.5-flash\n"},
			wantErr: `models[0] must be a non-empty model id`,
		},
		{
			name:    "suffix wildcard",
			models:  []string{"gemini-*"},
			wantErr: `contains a wildcard`,
		},
		{
			name:    "bare wildcard",
			models:  []string{"*"},
			wantErr: `contains a wildcard`,
		},
		{
			name:    "single-character wildcard",
			models:  []string{"gpt-4?"},
			wantErr: `contains a wildcard`,
		},
		{
			name:    "duplicate",
			models:  []string{"gemini-2.5-flash", "gemini-2.5-pro", "gemini-2.5-flash"},
			wantErr: `models[2] "gemini-2.5-flash" is listed more than once`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{
				Strategy: StrategyConfig{Mode: ModeSingle},
				Targets:  []Target{{VirtualKey: "gemini", Models: tt.models}},
			}
			err := ValidateConfig(cfg)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("ValidateConfig() = %v, want nil", err)
			case tt.wantErr != "" && err == nil:
				t.Fatalf("ValidateConfig() = nil, want error containing %q", tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Fatalf("ValidateConfig() = %v, want error containing %q", err, tt.wantErr)
			}
			if tt.wantErr != "" && !strings.Contains(err.Error(), `target "gemini"`) {
				t.Errorf("error %v does not name the offending target", err)
			}
		})
	}
}

// TestValidateTargetModelsAcrossTargets keeps the same id legal on more than one
// target. That is not a duplicate — it is how a model gets a fallback.
func TestValidateTargetModelsAcrossTargets(t *testing.T) {
	cfg := Config{
		Strategy: StrategyConfig{Mode: ModeFallback},
		Targets: []Target{
			{VirtualKey: "gemini", Models: []string{"shared-model"}},
			{VirtualKey: "cohere", Models: []string{"shared-model"}},
		},
	}
	if err := ValidateConfig(cfg); err != nil {
		t.Fatalf("ValidateConfig() = %v, want nil: one model may be served by several targets", err)
	}
}
