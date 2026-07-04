package anthropicwire

import (
	"context"
	"testing"
)

func f64(v float64) *float64 { return &v }

func TestClampTemperature(t *testing.T) {
	cases := []struct {
		name string
		in   *float64
		want *float64 // nil means "same pointer expected"
	}{
		{"nil passes through", nil, nil},
		{"in range passes through", f64(0.7), f64(0.7)},
		{"exactly one passes through", f64(1.0), f64(1.0)},
		{"above range clamps to one", f64(1.8), f64(1.0)},
		{"openai max clamps to one", f64(2.0), f64(1.0)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClampTemperature(context.Background(), "anthropic", "claude", tc.in)
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("got %v, want nil", *got)
			case tc.want == nil:
				// ok
			case got == nil:
				t.Fatalf("got nil, want %v", *tc.want)
			case *got != *tc.want:
				t.Fatalf("got %v, want %v", *got, *tc.want)
			}
			// Immutability: a clamp must not mutate the caller's value.
			if tc.in != nil && *tc.in > maxAnthropicTemperature && got == tc.in {
				t.Fatal("clamp mutated caller's pointer")
			}
		})
	}
}
