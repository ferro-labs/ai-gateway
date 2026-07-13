package envref

import (
	"os"
	"strings"
	"testing"
)

func TestExpand(t *testing.T) {
	t.Setenv("ENVREF_SET", "value")

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"braced reference is substituted", "${ENVREF_SET}", "value"},
		{"reference inside surrounding text", "Bearer ${ENVREF_SET}", "Bearer value"},
		{"bare dollar-name is NOT a reference", "$ENVREF_SET", "$ENVREF_SET"},
		{"leading dollar in a blocked word", "$100", "$100"},
		{"doubled dollars in a password", "pa$$w0rd", "pa$$w0rd"},
		{"dollar inside a sentence", "costs $5 per 1M", "costs $5 per 1M"},
		{"trailing dollar", "cash$", "cash$"},
		{"no references at all", "plain", "plain"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Expand(tt.in)
			if err != nil {
				t.Fatalf("Expand(%q): %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("Expand(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestExpand_UndefinedVariableIsAnError(t *testing.T) {
	if err := os.Unsetenv("ENVREF_DEFINITELY_UNSET"); err != nil {
		t.Fatalf("unsetenv: %v", err)
	}
	_, err := Expand("Bearer ${ENVREF_DEFINITELY_UNSET}")
	if err == nil {
		t.Fatal("an undefined ${VAR} must error, not silently produce an empty secret")
	}
	if !strings.Contains(err.Error(), "ENVREF_DEFINITELY_UNSET") {
		t.Errorf("the error must name the variable, got: %v", err)
	}
}

func TestAnyMap_RecursesAndDoesNotMutateInput(t *testing.T) {
	t.Setenv("ENVREF_SET", "value")

	in := map[string]any{ //nolint:gosec // G101: unresolved ${VAR} references, not credentials
		"token":  "${ENVREF_SET}",
		"nested": map[string]any{"inner": "${ENVREF_SET}"},
		"list":   []any{"${ENVREF_SET}", 7},
		"words":  []string{"$100", "${ENVREF_SET}"},
	}
	out, err := AnyMap(in)
	if err != nil {
		t.Fatalf("AnyMap: %v", err)
	}

	if out["token"] != "value" {
		t.Errorf("token = %v, want value", out["token"])
	}
	if got := out["nested"].(map[string]any)["inner"]; got != "value" {
		t.Errorf("nested.inner = %v, want value", got)
	}
	if got := out["list"].([]any)[0]; got != "value" {
		t.Errorf("list[0] = %v, want value", got)
	}
	if got := out["words"].([]string); got[0] != "$100" || got[1] != "value" {
		t.Errorf("words = %v, want [$100 value]", got)
	}

	// The caller's Config must keep its references — resolution returns a copy.
	if in["token"] != "${ENVREF_SET}" {
		t.Errorf("input was mutated: token = %v; the Config must retain ${VAR}", in["token"])
	}
	if got := in["nested"].(map[string]any)["inner"]; got != "${ENVREF_SET}" {
		t.Errorf("nested input was mutated: %v", got)
	}
}
