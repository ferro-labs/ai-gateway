package core

import "testing"

func TestNormalizeEmbeddingInput(t *testing.T) {
	// bare string is preserved as a string (not flattened to []string).
	got, err := NormalizeEmbeddingInput("hello")
	if err != nil {
		t.Fatalf("string: %v", err)
	}
	if s, ok := got.(string); !ok || s != "hello" {
		t.Errorf("string input = %#v, want bare string \"hello\"", got)
	}

	// []any of strings coerces to []string.
	got, err = NormalizeEmbeddingInput([]any{"a", "b"})
	if err != nil {
		t.Fatalf("[]any: %v", err)
	}
	if s, ok := got.([]string); !ok || len(s) != 2 || s[0] != "a" {
		t.Errorf("[]any input = %#v, want []string{a,b}", got)
	}

	for name, in := range map[string]any{
		"empty []string": []string{},
		"empty []any":    []any{},
		"nil":            nil,
		"non-string":     []any{1},
		"unsupported":    42,
	} {
		if _, err := NormalizeEmbeddingInput(in); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
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
