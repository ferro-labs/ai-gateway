// Package plugincfg provides shared helpers for decoding plugin
// configuration values.
package plugincfg

import "fmt"

// ToFloat64 converts a configuration value to float64, accepting float64, int, or int64.
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
