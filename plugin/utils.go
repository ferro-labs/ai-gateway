package plugin

import "fmt"

// StringSetting reads an optional string out of a plugin's config map, keeping
// an ABSENT key and a MALFORMED one apart.
//
// A nil value — what both a missing key and an explicitly null one decode to —
// means the key was not set, so the caller takes its default and no error is
// reported. A value of any other type is a load error naming the key: reading
// `action: 1` as "not set" leaves a plugin that starts, reports itself enabled
// in the catalog, and quietly enforces the default the operator was writing
// that line to override.
//
// Call it on the value, not the map — plugin/catalog_test.go discovers which
// keys a plugin reads from the literal config["key"] text in its source, so the
// subscript has to stay at the call site.
func StringSetting(raw any, key string) (string, error) {
	if raw == nil {
		return "", nil
	}
	s, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string, got %T", key, raw)
	}
	return s, nil
}

// ListSetting reads an optional list out of a plugin's config map on the same
// terms as StringSetting: absent takes the caller's default, malformed is a
// load error naming the key. A caller that has to tell "absent" from "present
// and empty" — the two mean different things for a selector — checks presence
// itself and passes the value here only to type it.
func ListSetting(raw any, key string) ([]any, error) {
	if raw == nil {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be a list, got %T", key, raw)
	}
	return list, nil
}

// ToFloat64 converts a plugin configuration value to float64, accepting
// float64, int, or int64.
func ToFloat64(v any) (float64, error) {
	switch val := v.(type) {
	case float64:
		return val, nil
	case int:
		return float64(val), nil
	case int64:
		return float64(val), nil
	default:
		return 0, fmt.Errorf("must be a number, got %T", v)
	}
}
