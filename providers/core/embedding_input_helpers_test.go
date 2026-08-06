package core

import (
	"errors"
	"net/http"
	"reflect"
	"strings"
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

// TestValidateEmbeddingEncodingFormat_Rejects400 pins the classification of the
// rejection, not just its existence. A bare error carries no status: it reaches
// the caller as a 500 saying the gateway is broken, and shouldRetry reads a
// status-less error as a transport failure and spends the whole retry budget on
// a value that can never become valid. All fifteen callers of this helper
// inherit the answer from here.
func TestValidateEmbeddingEncodingFormat_Rejects400(t *testing.T) {
	for _, bad := range []string{"base64", "int8", "FLOAT"} {
		t.Run(bad, func(t *testing.T) {
			err := ValidateEmbeddingEncodingFormat(bad)
			if err == nil {
				t.Fatalf("format %q: expected error, got nil", bad)
			}
			if got := ParseStatusCode(err); got != http.StatusBadRequest {
				t.Errorf("ParseStatusCode(%v) = %d, want %d", err, got, http.StatusBadRequest)
			}
			var statusErr *HTTPStatusError
			if !errors.As(err, &statusErr) {
				t.Fatalf("error is %T, want *HTTPStatusError", err)
			}
			// The rejected VALUE is the only actionable detail the caller gets.
			if !strings.Contains(statusErr.Message, bad) {
				t.Errorf("caller-facing message = %q, want it to name %q", statusErr.Message, bad)
			}
			// No upstream produced this, so no provider is named at it.
			if statusErr.Provider != "" {
				t.Errorf("Provider = %q, want empty for a gateway-side refusal", statusErr.Provider)
			}
		})
	}
}
