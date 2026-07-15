package tracingpolicy

import (
	"strings"
	"testing"
)

func TestValidatePrivacyLevel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		level   string
		wantErr bool
	}{
		{name: "empty is accepted as the default", level: "", wantErr: false},
		{name: "none is valid", level: PrivacyLevelNone, wantErr: false},
		{name: "metadata is valid", level: PrivacyLevelMetadata, wantErr: false},
		{name: "full is valid", level: PrivacyLevelFull, wantErr: false},
		{name: "unknown level is rejected", level: "verbose", wantErr: true},
		{name: "validation is case-sensitive", level: "Full", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidatePrivacyLevel(tt.level)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidatePrivacyLevel(%q) error = %v, wantErr %v", tt.level, err, tt.wantErr)
			}
			if tt.wantErr && !strings.Contains(err.Error(), tt.level) {
				t.Errorf("error %q should name the offending level %q", err, tt.level)
			}
		})
	}
}
