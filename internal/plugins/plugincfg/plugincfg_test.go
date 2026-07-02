package plugincfg

import "testing"

func TestToFloat64(t *testing.T) {
	tests := []struct {
		name    string
		input   any
		want    float64
		wantErr bool
	}{
		{name: "float64", input: float64(42.5), want: 42.5},
		{name: "int", input: int(42), want: 42},
		{name: "int64", input: int64(42), want: 42},
		{name: "string is unsupported", input: "42", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ToFloat64(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ToFloat64(%v) expected error, got nil", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("ToFloat64(%v) unexpected error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("ToFloat64(%v) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
