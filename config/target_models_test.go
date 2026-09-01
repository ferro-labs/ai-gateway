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

func TestValidateTargetIdentity(t *testing.T) {
	tests := []struct {
		name    string
		targets []Target
		wantErr string
	}{
		{name: "empty", targets: []Target{{}}, wantErr: "virtual_key must not be empty"},
		{name: "duplicate", targets: []Target{{VirtualKey: "openai"}, {VirtualKey: "openai"}}, wantErr: `virtual_key "openai" is listed more than once`},
		{name: "unique", targets: []Target{{VirtualKey: "openai"}, {VirtualKey: "anthropic"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateConfig(Config{Strategy: StrategyConfig{Mode: ModeFallback}, Targets: tt.targets})
			if tt.wantErr == "" && err != nil {
				t.Fatalf("ValidateConfig() = %v, want nil", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("ValidateConfig() = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateTargetModelMap(t *testing.T) {
	tests := []struct {
		name     string
		modelMap map[string]string
		aliases  map[string]string
		wantErr  string
	}{
		{name: "omitted"},
		{name: "exact mapping", modelMap: map[string]string{"support-chat": "gpt-4o"}},
		{name: "empty key", modelMap: map[string]string{"": "gpt-4o"}, wantErr: "model_map key must be a non-empty model id"},
		{name: "key whitespace", modelMap: map[string]string{" support-chat": "gpt-4o"}, wantErr: "model_map key"},
		{name: "key wildcard", modelMap: map[string]string{"support-*": "gpt-4o"}, wantErr: "contains a wildcard"},
		{name: "empty value", modelMap: map[string]string{"support-chat": ""}, wantErr: `model_map["support-chat"] must be a non-empty model id`},
		{name: "value whitespace", modelMap: map[string]string{"support-chat": " gpt-4o"}, wantErr: `model_map["support-chat"]`},
		{name: "value wildcard", modelMap: map[string]string{"support-chat": "gpt-*"}, wantErr: "contains a wildcard"},
		{name: "global alias key", modelMap: map[string]string{"fast": "gpt-4o"}, aliases: map[string]string{"fast": "gpt-4o-mini"}, wantErr: `model_map key "fast" is unreachable because it is a global alias`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{
				Strategy: StrategyConfig{Mode: ModeSingle},
				Targets:  []Target{{VirtualKey: "openai", ModelMap: tt.modelMap}},
				Aliases:  tt.aliases,
			}
			err := ValidateConfig(cfg)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("ValidateConfig() = %v, want nil", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("ValidateConfig() = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateTargetModelMapAcrossTargets(t *testing.T) {
	cfg := Config{
		Strategy: StrategyConfig{Mode: ModeFallback},
		Targets: []Target{
			{VirtualKey: "openai", ModelMap: map[string]string{"support-chat": "gpt-4o"}},
			{VirtualKey: "anthropic", ModelMap: map[string]string{"support-chat": "claude-sonnet-4"}},
		},
	}
	if err := ValidateConfig(cfg); err != nil {
		t.Fatalf("ValidateConfig() = %v, want nil", err)
	}
}
