package core

import (
	"reflect"
	"testing"
)

// TestNormalizeEmbeddingInput is the table-driven contract for the polymorphic
// Input: bare string and []string keep their wire form, []any of strings is
// coerced to []string, and empty arrays / nil / unsupported types are rejected.
func TestNormalizeEmbeddingInput(t *testing.T) {
	tests := []struct {
		name    string
		input   any
		want    any
		wantErr bool
	}{
		{name: "bare string preserved", input: "hello", want: "hello"},
		{name: "empty string is valid", input: "", want: ""},
		{name: "string slice preserved", input: []string{"a", "b"}, want: []string{"a", "b"}},
		{name: "any slice of strings coerced", input: []any{"a", "b"}, want: []string{"a", "b"}},
		{name: "empty string slice rejected", input: []string{}, wantErr: true},
		{name: "empty any slice rejected", input: []any{}, wantErr: true},
		{name: "any slice with non-string rejected", input: []any{"a", 1}, wantErr: true},
		{name: "nil rejected", input: nil, wantErr: true},
		{name: "unsupported type rejected", input: 42, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeEmbeddingInput(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (result=%#v)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestValidateEmbeddingEncodingFormat(t *testing.T) {
	for _, ok := range []string{"", "float"} {
		if err := ValidateEmbeddingEncodingFormat(ok); err != nil {
			t.Errorf("format %q: unexpected error %v", ok, err)
		}
	}
	for _, bad := range []string{"base64", "int8", "FLOAT"} {
		if err := ValidateEmbeddingEncodingFormat(bad); err == nil {
			t.Errorf("format %q: expected error, got nil", bad)
		}
	}
}
