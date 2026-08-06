package repository

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/ferro-labs/ai-gateway/config"
)

// TestRedactedSecretField_EveryStringLeaf is the guard on the other direction.
//
// GET replaces a secret with a placeholder, so a PUT carrying one back would
// overwrite the live credential with the literal text "[REDACTED]" — no error,
// no way to recover the original. RedactedSecretField refuses such a body, and
// it has to refuse it wherever the token sits, because widening what the
// scrubber withholds widens where a token can come back from.
//
// Rather than list the positions, this plants the token at every string leaf
// the config schema reaches, one at a time, and requires each to be refused. A
// field added to the schema later is covered with no edit here.
func TestRedactedSecretField_EveryStringLeaf(t *testing.T) {
	var cfg config.Config
	leaves := 0

	plantEachString(reflect.ValueOf(&cfg).Elem(), "", func(path string) {
		leaves++
		if field := RedactedSecretField(cfg); field == "" {
			t.Errorf("a redaction token at %s was not refused", path)
		}
	})

	// A floor rather than an exact count: the schema gains fields, and a test
	// that has to be edited for each one stops being run honestly. Zero, or a
	// collapse to a handful, means the walk stopped reaching.
	t.Logf("planted a token at %d string leaves", leaves)
	if leaves < 30 {
		t.Fatalf("reached %d string leaves, want the whole schema", leaves)
	}
}

// TestRedactedSecretField_PartialToken covers the shape a scrubbed value comes
// back in when only part of it was secret: the token arrives surrounded by the
// text it was cut from, so refusing only a bare placeholder would let precisely
// those through.
func TestRedactedSecretField_PartialToken(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.Config
		want string
	}{
		{
			name: "inside a url query",
			cfg:  config.Config{Observability: config.ObservabilityConfig{Tracing: config.TracingConfig{Endpoint: "https://h/?token=" + redactedPlaceholder}}},
			want: "observability.tracing.endpoint",
		},
		{
			name: "inside a nested array in a plugin config",
			cfg: config.Config{Plugins: []config.PluginConfig{{Name: "budget", Config: map[string]any{
				"tiers": []any{map[string]any{"keys": []any{"real", "Bearer [REDACTED_BEARER_TOKEN]"}}},
			}}}},
			want: "plugins[0].config",
		},
		{
			name: "inside a value in a withheld map",
			cfg:  config.Config{Aliases: map[string]string{"fast": "gpt-4o-" + redactedPlaceholder}},
			want: "aliases",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RedactedSecretField(tc.cfg); got != tc.want {
				t.Fatalf("RedactedSecretField = %q, want %q", got, tc.want)
			}
		})
	}
}

// plantEachString writes redactedPlaceholder into each string leaf under v in
// turn, calls check, and clears it again, so exactly one leaf carries the token
// at a time. Containers are materialised on the way down and left in place
// holding zero values.
//
// It walks the fields redactedPath walks — those with a serialized name — so
// the two cover the same leaves.
func plantEachString(v reflect.Value, path string, check func(path string)) {
	switch v.Kind() {
	case reflect.String:
		v.SetString(redactedPlaceholder)
		check(path)
		v.SetString("")
	case reflect.Struct:
		for i := range v.NumField() {
			name, ok := jsonFieldName(v.Type().Field(i))
			if !ok {
				continue
			}
			plantEachString(v.Field(i), joinPath(path, name), check)
		}
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		plantEachString(v.Index(0), fmt.Sprintf("%s[0]", path), check)
	case reflect.Map:
		if !reflect.TypeOf("").AssignableTo(v.Type().Elem()) {
			return
		}
		v.Set(reflect.MakeMap(v.Type()))
		key := reflect.ValueOf("planted")
		v.SetMapIndex(key, reflect.ValueOf(redactedPlaceholder))
		check(path)
		v.SetMapIndex(key, reflect.Value{})
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		plantEachString(v.Elem(), path, check)
	}
}
